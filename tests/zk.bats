#!/usr/bin/bats

@test "Prepare" {
  [[ -f ./hap.config  ]] && rm -f ./hap.config || true
  kill -9  $(pgrep -af ./haproxy.pid | awk '{print $1}') || true
  kill -9  $(pgrep -af tests_config_sed.yml | awk '{print $1}') || true
  sed -e 's#%PWD%#'$PWD'#' tests_config.yml > tests_config_sed.yml
  ../dist/synapse-v0-linux-amd64/synapse tests_config_sed.yml -L DEBUG >&2 2> ./synapse.log &
  sleep 1
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/1 || true
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/2 || true
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/4 || true
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/5 || true
  run pgrep -af tests_config_sed.yml
  [ "$status" -eq 0 ]
}

@test "Add a server" {
  zookeepercli --servers 127.0.0.1:2181 -c create /tmp/lol/1 \
            '{"available":true,"name":"test1","host":"127.0.0.1","port":80,"weight":233}'
  sleep 4
  cat hap.config >&2
  run cat hap.config
  [ "$status" -eq 0 ]
  [[ "${lines[@]}" =~ "test1 127.0.0.1:80 weight 233" ]]
  result=$(echo -e 'show servers state tmp_lol_1' | nc -U $PWD/haproxy.sock)
  echo "$result" >&2
  # 2 = ready
  [[ "${result}" =~ "test1 127.0.0.1 2" ]]
}

@test "Add a 2nd server" {
  zookeepercli --servers 127.0.0.1:2181 -c create /tmp/lol/2 \
            '{"available":true,"name":"test2","host":"127.0.0.1","port":80,"weight":111}'
  sleep 4
  cat hap.config >&2
  run cat hap.config
  [ "$status" -eq 0 ]
  [[ "${lines[@]}" =~ "test2 127.0.0.1:80 weight 111" ]]
  result=$(echo -e 'show servers state tmp_lol_1' | nc -U $PWD/haproxy.sock)
  echo "$result" >&2
  # 2 = ready
  [[ "${result}" =~ "test2 127.0.0.1 2" ]]
}

@test "Remove a server" {
  zookeepercli --servers 127.0.0.1:2181 -c rm /tmp/lol/1
  sleep 5
  cat hap.config >&2
  run cat hap.config
  [ "$status" -eq 0 ]
  cat hap.config | grep "test1 127.0.0.1:80 weight 233.*disabled"
  status=$?
  [ "$status" -eq 0 ]
  echo "$result" >&2
  result=$(echo -e 'show servers state tmp_lol_1' | nc -U $PWD/haproxy.sock)
  # 0 1 = maint
  [[ "${result}" =~ "test1 127.0.0.1 0 1" ]]
  sleep 1
}

@test "ReAdd a server" {
  cat hap.config > hap.bkp
  zookeepercli --servers 127.0.0.1:2181 -c create /tmp/lol/4 \
            '{"available":true,"name":"test4","host":"127.0.0.1","port":80,"weight":222}'
  sleep 4
  diff hap.readd hap.config || true >&2
  run cat hap.config
  [ "$status" -eq 0 ]
  [[ "${lines[@]}" =~ "test4 127.0.0.1:80 weight 222" ]]
  result=$(echo -e 'show servers state tmp_lol_1' | nc -U $PWD/haproxy.sock)
  echo "$result" >&2
  # 2 0 = ready
  [[ "${result}" =~ "test4 127.0.0.1 2" ]]
  # 0 1 = maint
  [[ "${result}" =~ "test1 127.0.0.1 0 1" ]]
}

@test "ReReAdd a server" {
  cat hap.config > hap.bkp
  zookeepercli --servers 127.0.0.1:2181 -c create /tmp/lol/5 \
            '{"available":true,"name":"test5","host":"127.0.0.1","port":80,"weight":111}'
  sleep 4
  diff hap.readd hap.config || true >&2
  run cat hap.config
  [ "$status" -eq 0 ]
  [[ "${lines[@]}" =~ "test5 127.0.0.1:80 weight 111" ]]
  result=$(echo -e 'show servers state tmp_lol_1' | nc -U $PWD/haproxy.sock)
  echo "$result" >&2
  # 2 0 = ready
  [[ "${result}" =~ "test5 127.0.0.1 2" ]]
  # 0 1 = maint
  [[ "${result}" =~ "test1 127.0.0.1 0 1" ]]
}

@test "Teardown" {
  kill -9  $(pgrep -af ./haproxy.pid | awk '{print $1}')
  kill -9  $(pgrep -af tests_config_sed.yml | awk '{print $1}')

  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/1 || true
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/2 || true
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/3 || true
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/4 || true
  zookeepercli --force --servers 127.0.0.1:2181 -c rm /tmp/lol/5 || true
  run pgrep -af tests_config_sed.yml
  [ "$status" -eq 1 ]
}