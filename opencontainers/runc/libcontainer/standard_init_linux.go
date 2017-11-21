// +build linux

package libcontainer

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/opencontainers/runc/libcontainer/apparmor"
	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/opencontainers/runc/libcontainer/keys"
	"github.com/opencontainers/runc/libcontainer/label"
	"github.com/opencontainers/runc/libcontainer/seccomp"
	"github.com/opencontainers/runc/libcontainer/system"
)

//newContainerInit 中构造使用
type linuxStandardInit struct {
	//init进程和其他进程通信的pipe //本进程和init进程通过管道，也就是 initProcess 中的 parentPipe 和 childPipe 通信
	pipe       io.ReadWriteCloser
	parentPid  int
	stateDirFD int
	config     *initConfig
}

func (l *linuxStandardInit) getSessionRingParams() (string, uint32, uint32) {
	var newperms uint32

	if l.config.Config.Namespaces.Contains(configs.NEWUSER) {
		// with user ns we need 'other' search permissions
		newperms = 0x8
	} else {
		// without user ns we need 'UID' search permissions
		newperms = 0x80000
	}

	// create a unique per session container name that we can
	// join in setns; however, other containers can also join it
	return fmt.Sprintf("_ses.%s", l.config.ContainerId), 0xffffffff, newperms
}

// PR_SET_NO_NEW_PRIVS isn't exposed in Golang so we define it ourselves copying the value
// the kernel
const PR_SET_NO_NEW_PRIVS = 0x26
//runc init 进程执行，  (l *LinuxFactory) StartInitialization() 中调用
func (l *linuxStandardInit) Init() error {
	if !l.config.Config.NoNewKeyring {
		ringname, keepperms, newperms := l.getSessionRingParams()

		// do not inherit the parent's session keyring
		sessKeyId, err := keys.JoinSessionKeyring(ringname)
		if err != nil {
			return err
		}
		// make session keyring searcheable
		if err := keys.ModKeyringPerm(sessKeyId, keepperms, newperms); err != nil {
			return err
		}
	}

	var console *linuxConsole
	if l.config.Console != "" {
		console = newConsoleFromPath(l.config.Console)
		if err := console.dupStdio(); err != nil {
			return err
		}
	}
	if console != nil {
		if err := system.Setctty(); err != nil {
			return err
		}
	}
	if err := setupNetwork(l.config); err != nil {
		return err
	}
	if err := setupRoute(l.config.Config); err != nil {
		return err
	}

	label.Init()
	// InitializeMountNamespace() can be executed only for a new mount namespace
	if l.config.Config.Namespaces.Contains(configs.NEWNS) {
		if err := setupRootfs(l.config.Config, console, l.pipe); err != nil {
			return err
		}
	}
	if hostname := l.config.Config.Hostname; hostname != "" {
		if err := syscall.Sethostname([]byte(hostname)); err != nil {
			return err
		}
	}
	if err := apparmor.ApplyProfile(l.config.AppArmorProfile); err != nil {
		return err
	}
	if err := label.SetProcessLabel(l.config.ProcessLabel); err != nil {
		return err
	}

	for key, value := range l.config.Config.Sysctl {
		if err := writeSystemProperty(key, value); err != nil {
			return err
		}
	}

	//重新挂载ReadonlyPaths，在配置文件中为/proc/asound,/proc/bus, /proc/fs等等
	for _, path := range l.config.Config.ReadonlyPaths {
		if err := remountReadonly(path); err != nil {
			return err
		}
	}
	for _, path := range l.config.Config.MaskPaths {
		if err := maskPath(path); err != nil {
			return err
		}
	}
	pdeath, err := system.GetParentDeathSignal()
	if err != nil {
		return err
	}
	if l.config.NoNewPrivileges {
		if err := system.Prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			return err
		}
	}
	// Tell our parent that we're ready to Execv. This must be done before the
	// Seccomp rules have been applied, because we need to be able to read and
	// write to a socket.
	// 告诉父进程容器可以执行Execv了, 从父进程来看，create已经完成了
	if err := syncParentReady(l.pipe); err != nil {
		return err
	}
	// Without NoNewPrivileges seccomp is a privileged operation, so we need to
	// do this before dropping capabilities; otherwise do it as late as possible
	// just before execve so as few syscalls take place after it as possible.
	if l.config.Config.Seccomp != nil && !l.config.NoNewPrivileges {
		if err := seccomp.InitSeccomp(l.config.Config.Seccomp); err != nil {
			return err
		}
	}
	if err := finalizeNamespace(l.config); err != nil {
		return err
	}
	// finalizeNamespace can change user/group which clears the parent death
	// signal, so we restore it here.
	if err := pdeath.Restore(); err != nil {
		return err
	}
	// compare the parent from the initial start of the init process and make sure that it did not change.
	// if the parent changes that means it died and we were reparented to something else so we should
	// just kill ourself and not cause problems for someone else.
	if syscall.Getppid() != l.parentPid { //判断syscall.Getppid()和l.parentPid是否相等,正常情况应该相等，init的父进程为 runc create进程
		return syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
	}
	// check for the arg before waiting to make sure it exists and it is returned
	// as a create time error.
	name, err := exec.LookPath(l.config.Args[0])
	if err != nil {
		return err
	}
	// close the pipe to signal that we have completed our init.
	l.pipe.Close()
	// wait for the fifo to be opened on the other side before
	// exec'ing the users process.  其实此处就是在等待start命令
	fd, err := syscall.Openat(l.stateDirFD, execFifoFilename, os.O_WRONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return newSystemErrorWithCause(err, "openat exec fifo")
	}
	if _, err := syscall.Write(fd, []byte("0")); err != nil {
		return newSystemErrorWithCause(err, "write 0 exec fifo")
	}
	if l.config.Config.Seccomp != nil && l.config.NoNewPrivileges {
		if err := seccomp.InitSeccomp(l.config.Config.Seccomp); err != nil {
			return newSystemErrorWithCause(err, "init seccomp")
		}
	}
	// close the statedir fd before exec because the kernel resets dumpable in the wrong order
	// https://github.com/torvalds/linux/blob/v4.9/fs/exec.c#L1290-L1318
	syscall.Close(l.stateDirFD)

	/*
	 "args": [
	      "/sbin/init"
	  ],
	*/
	//执行容器命令  config.json中的 args 配置项
	if err := syscall.Exec(name, l.config.Args[0:], os.Environ()); err != nil {
		return newSystemErrorWithCause(err, "exec user process")
	}
	return nil
}