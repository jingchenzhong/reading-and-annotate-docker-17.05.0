#!/usr/bin/env bats

load helpers

function setup() {
  teardown_busybox
  setup_busybox
}

function teardown() {
  teardown_busybox
}

@test "runc exec" {
  # run busybox detached
  runc run -d --console /dev/pts/ptmx test_busybox
  [ "$status" -eq 0 ]

  wait_for_container 15 1 test_busybox

  runc exec test_busybox echo Hello from exec
  [ "$status" -eq 0 ]
  echo text echoed = "'""${output}""'"
  [[ "${output}" == *"Hello from exec"* ]]
}

@test "runc exec --pid-file" {
  # run busybox detached
  runc run -d --console /dev/pts/ptmx test_busybox
  [ "$status" -eq 0 ]

  wait_for_container 15 1 test_busybox

  runc exec --pid-file pid.txt test_busybox echo Hello from exec
  [ "$status" -eq 0 ]
  echo text echoed = "'""${output}""'"
  [[ "${output}" == *"Hello from exec"* ]]

  # check pid.txt was generated
  [ -e pid.txt ]

  run cat pid.txt
  [ "$status" -eq 0 ]
  [[ ${lines[0]} =~ [0-9]+ ]]
  [[ ${lines[0]} != $(__runc state test_busybox | jq '.pid') ]]
}

@test "runc exec ls -la" {
  # run busybox detached
  runc run -d --console /dev/pts/ptmx test_busybox
  [ "$status" -eq 0 ]

  wait_for_container 15 1 test_busybox

  runc exec test_busybox ls -la
  [ "$status" -eq 0 ]
  [[ ${lines[0]} == *"total"* ]]
  [[ ${lines[1]} == *"."* ]]
  [[ ${lines[2]} == *".."* ]]
}

@test "runc exec ls -la with --cwd" {
  # run busybox detached
  runc run -d --console /dev/pts/ptmx test_busybox
  [ "$status" -eq 0 ]

  wait_for_container 15 1 test_busybox

  runc exec --cwd /bin test_busybox pwd
  [ "$status" -eq 0 ]
  [[ ${output} == "/bin" ]]
}

@test "runc exec --env" {
  # run busybox detached
  runc run -d --console /dev/pts/ptmx test_busybox
  [ "$status" -eq 0 ]

  wait_for_container 15 1 test_busybox

  runc exec --env RUNC_EXEC_TEST=true test_busybox env
  [ "$status" -eq 0 ]

  [[ ${output} == *"RUNC_EXEC_TEST=true"* ]]
}

@test "runc exec --user" {
  # run busybox detached
  runc run -d --console /dev/pts/ptmx test_busybox
  [ "$status" -eq 0 ]

  wait_for_container 15 1 test_busybox

  runc exec --user 1000:1000 test_busybox id
  [ "$status" -eq 0 ]

  [[ ${output} == "uid=1000 gid=1000" ]]
}
