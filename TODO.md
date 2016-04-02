## To Do

### Short List

> Also see: https://trello.com/b/bOWTSxbO/blanket-dev

- list queued and non-queued tasks separately in ps and in html UI

- replace timeouts and sleeps in go routines with timers
    - https://mmcgrana.github.io/2012/09/go-by-example-timers-and-tickers.html
    - can cancel

- add tests for boltdb backend
    - https://godoc.org/github.com/gin-gonic/gin#CreateTestContext
    - add task, list in queue
    - add tasks, filter in queue
    - add task, get from queue with worker
    - move task through states with worker
    - task filtering tests
        - work on the db level, not http
- NOTES
    - all basic interactions + unit tests
    - can run worker interactions in a separate go routine

- discard first log line because might be partial

- view structured worker log

- follow some of the advice here
    - https://www.youtube.com/watch?v=29LLRKIL_TI
    - http://spf13.com/presentation/7-common-mistakes-in-go-2015/
    - define some more complex error types, and check types to define behavior
    - use more interfaces
    - use io.Reader and io.Writer esp.
    - define smaller interfaces and compose
    - use value methods (or just functions that accept an object) when you don't have to modify state
        - methods that accept a value instead of a pointer are threadsafe
    - use locks where it makes sense to make methods threadsafe
        - or just clearly mark an object as not thread safe, and let users add their own concurrency safety

- run sync + redirect
    - sync = hold open request until task is done
        - may want to allow the user to attach to a running task, wait until it completes, then redirect
        - http://www.slideshare.net/arschles/concurrency-patterns-48668399/24
            - context may be helpful
    - redirect = redirect to a given file produced as part of the task when finished

- allow multi-requests
    - delete multiple
    - archive multiple

- custom error types for 404s
    - http://blog.golang.org/error-handling-and-go
    - https://gobyexample.com/errors

- move all REST API endpoints to
    - `/api/v1`
    - allows UI to sit at base
    - base url `/` is just a list of the endpoints where UI plugins are installed
        - unless content type is json, in which case it is the configuration of the server
    - use gin api grouping for this


- allow user to add task types over HTTP instead of just reading from disk
    - add API endpoint to rescan from disk
        - items on disk cannot be updated on the UI
        - can copy a task from disk up to the UI
        - command line utility to post all templates in a given location to UI
            - overwrites whatever is on there
    - option to export from server to file
        - as a TOML file or a JSON file
        - TOML will lose comments in round trip
    - option to scan just this machine or set a flag in DB so all servers will rescan
    - kind of like how ES picks up configuration in a cluster
    - should be able to do this via JSON or TOML

- UX
    - airflow shows rendered template + log for completed task
    - also shows list of task details

- allow user to use a previous task as a template for a new task
    - just POST to /task/<id>/rerun
    - return id of the new task
    - we may need to store more information about the task to do this correctly
        - e.g. the difference between env variables set on the task itself and those inherited from the task type
    - add this as an action on the task detail page; actions
        - stop/delete
        - archive
        - Run another like this
    - put these next to "Basic Settings" in the UI

- controlling running tasks
    - If is task has passed its run time, unlock it and return to queue
    - stop / restart
    - configurable # restarts
        - # times allowed, whether they go in new directories
    - Check for orphaned tasks
        - in RUNNING state but over time / worker is down
    - may not want to do  "redo" not and instead make it easy to "launch another"
    - add background process that looks for orphaned jobs and cleans them up
        - need to be able to track that a job is a "re-run" of another job
        - also runNumber
            - starts at 1
    - terminology: timeouts / SLAs
        - http://pythonhosted.org/airflow/concepts.html#slas

- Allow configurable executors
    - see how supervisord does it (and docker)
        - http://supervisord.org/subprocess.html#subprocess-environment
    - copy command into tmp file; run with bash, zsh, bat
    - make it use a different executor depending on OS (put this in viper)
    - test on windows
    - e.g. http://ss64.com/nt/syntax-run.html
    - http://stackoverflow.com/questions/4571244/creating-a-bat-file-for-python-script
    - http://stackoverflow.com/questions/13008255/how-to-execute-a-simple-windows-dos-command-in-golang
    - add some files that compile OS specific variables
        - e.g. https://github.com/Sirupsen/logrus/blob/master/terminal_windows.go
    - can also define "base" executor and an array of arguments
        - like for docker

- set environment variables in task to make working within blanket easier
    - BLANKET_APP_TASK_ID
    - BLANKET_APP_RESULTS_DIRECTORY
    - BLANKET_APP_TASK_RESULTS_DIRECTORY
    - BLANKET_APP_WORKER_PID
    - BLANKET_APP_SERVER_PORT

- allow filling in files as templates
    - have glob patterns to match templates (relative to where they will be copied into)

- make some good examples
- put api calls into sub directories
- add ability to archive tasks and load from archive
    - archive is basically results directory + a `.blanket-task` file that defines the JSON for that task
    - archive utils
        - https://github.com/packer-community/winrmcp

- handle undo
    - deleting just archives
    - auto-clean up archives after X days
    - when archiving, delay a couple seconds
        - flash message at top of screen (like in the "undo send" in gmail), they can cancel easily
    - can keep all these actions in a queue of actions in the database that we go through every couple seconds
        - would still want to remove item from UI immediately first
    - basically the same as gmail's UI

- use Docker containers for builds
    - so we can use this:
        go build -race .
    - so users can build static files easily

- background tasks
    - manage with tickers
    - https://mmcgrana.github.io/2012/09/go-by-example-timers-and-tickers.html
    - use this for managing restart of failing tasks: https://github.com/thejerf/suture
- add status page like this
    - https://status.github.com/

- add TOC to README
    - ex: https://github.com/ory-am/hydra

### Larger Efforts

#### Worker cleanup

- assign a unique id to make tracking logfiles for past workers easier
- name logfile based on time of day
- allow pausing
- view worker status via ps / ls
    - keep list of workers in database so have reference to pids
- send worker logs to stdout addition to sending to a file
    - logrus makes this pretty simple: https://github.com/Sirupsen/logrus
- add ability to stop task and not just delete
- keep in the database in a "killed" state
    - keeps a log of its pid, location of logfiles, start and end time, etc.
    - allows the user to see the worker logs for the worker that ran that task
- check that the process with that pid is actually a worker started when you thought it was before you kill it
    - https://github.com/mitchellh/go-ps
- use ctx to clean up canceling of requests
    - https://vimeo.com/115309491
    - esp. log tailing
    - addesses local files in comments at min 29
        - could also use this: https://godoc.org/gopkg.in/tomb.v2
        - or: https://github.com/thejerf/suture

#### Data migrations

- Keep a configuration bucket in db that defines various properties, including versions
- Include a uri to run data migrations; each just a generic task that can do anything

- first few
    - defaultEnv -> execEnv
    - put results into directories with task name as top directory
        - allow the directory structure underneath to be configurable
            - e.g. dates, just ids, etc

#### Packaging

- installer
    - add an installer (esp. for windows) or package (for linux) that sets up config
- examples
    - https://github.com/influxdata/telegraf#installation
    - https://github.com/influxdata/telegraf/blob/master/scripts/build.py
        - uses fpm
    - https://fedoraproject.org/wiki/PackagingDrafts/Go
        - instructions for fedora / rhel / centos
- look into making it a service
    - https://github.com/kardianos/service
- https://packager.io/
    - tool to make packaging easier
    - free plan for open source projects
    - https://github.com/pkgr/installer
        - example of more complex pre and post install hooks
- py2rpm
    - may be a good thing to check out
    - https://github.com/harlowja/packtools
- http://stackoverflow.com/questions/15104089/packaging-golang-application-for-debian
    - recommends against using FPM for DEB packages
- homebrew
    - https://github.com/Homebrew/homebrew-binary

#### Testing

- factor out common test utilities to a set of shared utilities
- put these in their own package so they are only included in the binary during test runs, not during build
- check coverage
    - https://talks.golang.org/2014/testing.slide#9

Set up automated tests
- https://travis-ci.org/
    - handles makefiles fine
    - https://docs.travis-ci.com/user/languages/go
    - ex: https://github.com/streadway/amqp/blob/master/.travis.yml
    - ex: https://github.com/tsuru/tsuru/blob/master/.travis.yml
- https://www.appveyor.com/
    - windows CI
    - https://blog.klauspost.com/travisappveyor-ci-script-for-go/
- https://coveralls.io/
    - https://github.com/mattn/goveralls
    - has good simple setup instructions
- add docker container for running tests so users can test in isolated installation

https://divan.github.io/posts/integration_testing/
https://github.com/ory-am/dockertest
- integration tests with docker and go, with gin

- fs abstraction
    - https://github.com/spf13/afero#using-afero-for-testing
    - use this esp. for testing functions that copy files around

- may want to get rid of filesystem methods and instead use this:
    - https://github.com/Redundancy/go-sync

https://www.youtube.com/watch?v=ndmB0bj7eyw
- http tests
- testing process behavior

https://github.com/matryer/silk
- http tests driven by documentation

http://talks.golang.org/2012/10things.slide#8
- testing fs

https://peter.bourgon.org/go-in-production/#testing-and-validation
- soundcloud uses build tags, flags, and a integration_test.go file to do integration tests

- https://www.golang-book.com/books/intro/12
- https://golang.org/pkg/net/http/httptest/
- https://github.com/stretchr/testify
- esp. for glob copy method
    - move this to its own thing and open source it
    - include expanduser function (like in python)
        - right now we just replace `~`
        - we should instead replace `^~/` or `/~/` so we don't replace file names with ~ in them
- fuzz testing
    - https://github.com/dvyukov/go-fuzz
- https://golang.org/pkg/testing/
- https://golang.org/cmd/go/#hdr-Test_packages
- https://splice.com/blog/lesser-known-features-go-test/
    - can run in parallel
- https://golang.org/pkg/net/http/httptest/
- https://talks.golang.org/2014/testing.slide#1
- https://groups.google.com/forum/#!topic/golang-nuts/DARY7HY-pbY
    - testing http routes
- packages
    - https://github.com/onsi/ginkgo
    - https://github.com/stretchr/testify

- ensure it can run without a configuration file
    - it currently can run that way just fine

#### Documentation

- set up hugo to generate api docs
    - https://gohugo.io/overview/introduction/
    - render into a single page
        - https://gohugo.io/extras/toc/

- or maybe just read the docs
    - https://ringpop.readthedocs.org/en/latest/
    - https://github.com/uber/ringpop-common/tree/master/docs

- or maybe just a wiki
    - https://github.com/Netflix/zuul/wiki

#### Log cleanup

> Long running bash task
> for i in $(seq 1 3600); do echo "$(date)"; sleep 1; done

- allow combining stdout stderr
    - can set to the same file and go takes care of it
    - https://golang.org/pkg/os/exec/#Cmd
- fall back to polling logfile (with offset) if eventsource not available
    - http://caniuse.com/#feat=eventsource
- fixes to log streaming
    - if nothing attached to a stream for >5 minutes, close it
    - stdout and stderr logs
    - worker logs too
    - package up SSE log view into a directive
- multiple files + rotate function
    - http://stackoverflow.com/questions/28796021/how-can-i-log-in-golang-to-a-file-with-log-rotation
- hup to rotate
    - https://github.com/natefinch/lumberjack/blob/v2.0/rotate_test.go
- configurable logging verbosity
    - these are things that every task must provide, or it will be rejected
- better worker logs with SSE and json logs
    - can include little event cards for everything that happened, even highlight errors, provide search and filtering, aggregate events
- name worker logs based on
    - time it started
    - capabilities (tags)
    - http://localhost:3000/#/workers
    - pid is still helpful, but only when it is running, and we have that in the database


#### Performance and monitoring

- Stats / Performance
    - we want to make sure we're not growing in memory, CPU, goroutines, # file descriptors, etc.
    - put stats into their own bucket
    - each blob is its own packet of stats for a time window
    - scanning through this bucket we can quickly pull out the stats we need and make a plot
    - stat viz: http://www.tnoda.com/blog/2013-12-19, http://cmaurer.github.io/angularjs-nvd3-directives/sparkline.chart.html
- Add prometheus and expvar metrics
    - see logstore as an example
    - include expvar in core
    - make prometheus a plugin
        - /api/ext/prometheus
        - scrapes expvar metrics
            - https://godoc.org/github.com/prometheus/client_golang/prometheus#ExpvarCollector
    - alt
        - https://github.com/codahale/metrics
        - https://github.com/rcrowley/go-metrics
        - https://github.com/armon/go-metrics
- Add pprof metrics
    - see influxdb as an example
        - https://github.com/influxdata/influxdb/blob/master/services/httpd/handler.go#L173
    - https://github.com/DeanThompson/ginpprof
- Add-in for check_mk local checks
    - listens for status information and writes to a file
    - may want to just use python stuff that I already made
    - do this as a separate thing
        - just scrapes stats off monitoring endpoint like we with elasticsearch

#### Task Discovery

- allow the user to names more places to look for tasks
- look for anything that matches a certain pattern and keep a reference to it in the database as a available task type
- maybe ~/.blanket/task_types/
    - that could be a good place for configuration
    - need to define something that makes sense on lots of OSes, even ones where people have limited access


## Backlog

- add tools for dealing with corrupt database
    - list buckets
    - list ids of items in a bucket
    - operate on: id, range of ids, whole bucket
        - dump everything as json; stream out 1 per line
        - remove items
- godeps to lock in dependencies and avoid weird changes
    - https://github.com/sparrc/gdm
    - https://github.com/Masterminds/glide
- emails
    - send emails to a given account with information about a task
    - would be a good plugin
- clean up About page
    - link to docs
    - link to twitter, github
    - version information
- security
    - SSL
        - https://godoc.org/github.com/gin-gonic/gin#Engine.
        - maybe guide using letsencrypt
            - https://caddyserver.com/blog/caddy-0_8-released
            - https://caddyserver.com/blog/lets-encrypt-progress-report
            - https://caddyserver.com/docs/automatic-https
            - https://github.com/ericchiang/letsencrypt
            - https://github.com/xenolf/lego
        - would be nice to have a plugin to
            - fetch and initialize TLS
            - auto-renew TLS config for you
    - allow running on unix domain docker too
        - this would make it inaccessible off the user's machine
        - this prevents port collisions when running in docker containers too, like I saw with supervisor and using localhost:9001 in host mode
    - HTTP basic auth
        - https://godoc.org/github.com/gin-gonic/gin#BasicAuth
            - don't store username and password directly
            - store hashed password and compare that
        - https://godoc.org/golang.org/x/crypto/bcrypt
        - allow them to do username/password (basic auth) or generate API keys (X-BLANKET-API-KEY)
            - https://github.com/MaaSiveNet/maasive-py/blob/master/maasivepy/maasivepy.py#L243
        - https://github.com/gin-gonic/gin/blob/master/auth.go#L40
            - would be pretty easy to do a version of this that stores users in bolt and uses bcrypt for lookup
        - https://github.com/mholt/caddy/blob/master/middleware/basicauth/basicauth.go
            - example middleware
    - CSRF
        - https://github.com/codahale/charlie
    - secure cookies
        - https://github.com/codahale/safecookie
    - could keep session cookies in RAM
        - https://github.com/bpowers/seshcookie
            - or use something like this so app can stay stateless
        - flush to a blob in the configuration bucket every minute if any changes
        - also flush on shutdown
        - this way we can easily keep list of sessions up to date but still be resilient to restarts
    - allow the user to control the user account used to run a task of a particular type
        - that way they can lock it down
        - will need to make sure user account has access when copying files over
        - https://golang.org/pkg/os/#ProcAttr
        - https://golang.org/pkg/syscall/#SysProcAttr
        - https://golang.org/pkg/syscall/#Credential
        - yeah, you can set userid and group id
    - rate limiting
        - https://github.com/ulule/limiter
    - authentication through providers
        - https://github.com/markbates/goth
    - user accounts, http auth login, account types
        - https://github.com/xyproto/permissionbolt
            - uses boltdb for simple security
        - https://github.com/tonnerre/go-ldap
            - LDAP
    - rich RBAC system with OAuth
        - https://github.com/ory-am/hydra
        - should be easy to integrate because works as a RESTful API
- Option to leave task creation request open until task completes
    - would make testing easier
    - also adds it with super high priority so it is picked up fast
    - like this: https://github.com/celery/celery/issues/2275#issuecomment-56828471
- allow progress by writing to a .progress file (a single integer followed by an arbitrary string) in addition to curl
- general monitoring
    - https://github.com/shirou/gopsutil
    - total CPU and memory usage of everything
    - total disk space of all results
- cross compiling
    - should be able to combile for centos, ubuntu, windows, mac in 32bit/64bit versions all at once
    - http://dave.cheney.net/2015/08/22/cross-compilation-with-go-1-5
- command line completion
    - cobra has this built in, but will probably have to work with build system / makefile to get this right
    - package this up into RPM/Deb
- include weights for fair queuing
- use stacked bars to show the amt of tasks of each type that have failed, are in progress, or succeeded
    - http://getbootstrap.com/components/#progress-stacked
- include template extensions
    - https://github.com/leekchan/gtf
- multiple template formats
    - like mandrill: http://blog.mandrill.com/handlebars-for-templates-and-dynamic-content.html
    - https://github.com/aymerick/raymond
    - https://github.com/flosch/pongo2
    - https://github.com/lestrrat/go-xslate
    - https://github.com/benbjohnson/ego
    - https://github.com/sipin/gorazor
    - https://github.com/hashicorp/hil
    - ** this is an ideal candidate for plugin design
- autoscheduling of backups
    - https://github.com/boltdb/bolt#database-backups
    - list interval in config
    - when starting up, check if newest backup is too old, if so snapshot immediately
    - schedule next snapshot immediately after running first one
- pluggable queue / datastore
    - use amazon RDS (as queue and datastore) to start to make distributed large deployments easy
        - RDS would maintain information about instances connected, workers available
        - using RDS and https://golang.org/pkg/database/sql/ means any sql database is ok
    - each type just has to implement a certain interface
        - for most, send string, get back string
        - input string is request json, output string is response json
- pluggable result store
    - allow writing to a tmp directory and then storing on s3
    - this should probably just be part of the task, but uploading and then clearing the directory would be a common cool task
    - also a nil result store that will only keep output if the task fails
        - but the task could do that itself too
- Scheduling / future execution
    - https://godoc.org/github.com/robfig/cron
    - https://github.com/robfig/cron
    - https://github.com/jasonlvhit/gocron
    - should be able to set min start date for a task
    - periodic scheduling would be good too, though this is pretty easy with cron, so not a big deal
- Mark tasks so that only 1 version of the task can be running at a time
    - e.g. tasks operating on a spreadsheet
    - max_concurrent_tasks
- Task dependencies
    - similar to airflow and bamboo
- add `?v` argument to provide pretty printed output, like consul
    - http://stackoverflow.com/questions/19038598/how-can-i-pretty-print-json-using-go
- allow TOML file inheritance, starting with a different base task type
    - https://github.com/spf13/viper/blob/master/viper.go#L938
    - to start, everything is bash
- moving tasks in different states to different buckets
    - would make scanning to find new tasks faster if all ERROR/SUCCESS tasks weren't in the same place
- recommended tool for making your thing available
    - https://ngrok.com/
- Cookie cutter / quickstart
    - generates an example project for people to get started developing wrapper in various languages with docs included
- distributed:
    - Allow path to task information to be on a specific machine accessible over ssh
    - workers on other machines would be great
    - can start with single master
    - could do a distributed concensus thing so that nodes find each other; 1 is the master
        - replicates data operations to all downsteam followers (WAL tailing)
        - uses a merkle tree to ensure sections of database stay in sync: https://en.wikipedia.org/wiki/Merkle_tree
    - paths to data are prepended with ip
    - workers on each node can be launched with different capabilties
        - would have to have the exe running on each node so they could talk to each other
        - each one would need to be able to launch workers
    - ** we don't really want to recreate a distributed database, so single master is probably ok
        - can even add a command to switch master over to a different node
        - can lock server, database contents out over HTTP, switch the master to the new node, and start back up
        - if a node is not the master, all it does it forward requests onto the master node
- very cool
    - notifications on OSX
        - https://github.com/deckarep/gosx-notifier
    - chat support for OSS
        - https://gitter.im/
    - workflow support for github tickets
        - https://waffle.io/
    - menubar application
        - https://github.com/maxogden/menubar
        - could package this into distributions on windows and mac
    - terminal ui
        - https://github.com/gizak/termui
    - websockets in addition to sse
        - https://github.com/joewalnes/websocketd
    - live updating config
        - https://github.com/kelseyhightower/confd
    - progress bars when downloading plugins
        - https://github.com/gosuri/uiprogress
    - interactive set up script
        - https://github.com/segmentio/go-prompt
    - use unix domain sockets, named pipes if possible
        - if it doesn't need to be exposed, we shouldn't expose it
    - allow users to express dependencies as a dag
        - https://github.com/hashicorp/terraform/tree/master/dag

