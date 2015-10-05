# Blanket

A RESTy wrapper for services.

## Running

    go build .
    ./blanket -h

Running tests

    # For just a module
    go test ./tasks

    # For everything
    go test github.com/turtlemonvh/blanket/...

Docs

* https://golang.org/pkg/testing/
* https://golang.org/cmd/go/#hdr-Test_packages
* https://splice.com/blog/lesser-known-features-go-test/
    * can run in parallel
* https://golang.org/pkg/net/http/httptest/
* https://talks.golang.org/2014/testing.slide#1
* https://groups.google.com/forum/#!topic/golang-nuts/DARY7HY-pbY
    * testing http routes
* packages
    * https://github.com/onsi/ginkgo
    * https://github.com/stretchr/testify

## API


blanket server

- launches server
- option to run in unix socket instead of 0.0.0.0 or 127.0.0.1 for security
- option to run with tls

blanket worker -t <tags> -n <number>

- launches n workers with capabilities defined by (optional) tags
- also command to kill workers

blanket run <task> -d{<data>}

- run a task of the specified type, passing in a config file to parameterize

>>> Others

- list tasks
- list running jobs
- show curses interface for monitoring progress (top)
- list task types
- add/remove task type
- add/remove job



## To Do


- script to launch workers of a given type
    - option to run as daemon
    - windows daemon is not supported
        - https://github.com/takama/daemon/blob/master/daemon_windows.go
    - may just want to reach back around and call yourself
        - http://stackoverflow.com/questions/4850489/get-the-current-process-executable-name-in-go
        - http://stackoverflow.com/questions/12090170/go-find-the-path-to-the-executable
        - https://github.com/kardianos/osext
- make sure can remove them with main script
    - should be able to list workers and delete them by tag, pid, or all of them
- log their information (tags, status updates) to a file
    - blanket_worker.<pid>.log


- Script to load fake data into database
- Directory for scripts
    - https://github.com/hashicorp/consul/blob/master/commands.go
        - directs to scripts
    - https://github.com/hashicorp/consul/blob/master/command/event.go
        - actual script
- Add tests
    - see test for just about every type here: https://github.com/hashicorp/consul/tree/master/consul



https://github.com/hashicorp/consul/blob/master/commands.go
- a generic factor to create a channel to listen to shutdown signals for all commands of relevant type
- the most complicated "agent" command is in its own folder

https://github.com/hashicorp/consul/blob/master/main.go
- starts up, `commands` file creates "Commands" object
- starts with all logs being discarded

