# Blanket

A RESTy wrapper for services.

## Quick Start

    go build .
    ./blanket -h

View tasks

    curl -s -X GET localhost:8773/task/ | python -mjson.tool
    # OR
    ./blanket ps -a

Add a new task

    # Add 3 different tasks with different params
    curl -s -X POST localhost:8773/task/ -d'{"type": "bash_task", "environment": {"PANDAS": "four", "Frogs": 5, "CATS": 2}}'
    curl -X POST localhost:8773/task/ -d'{"type": "bash_task", "environment": {"PANDAS": "four", "Frogs": 5, "CATS": 2, "DEFAULT_COMMAND": "cd ~ && ls -lah"}}'
    curl -X POST localhost:8773/task/ -d'{"type": "python_hello", "environment": {"frogs": 5, "CATS": 2}}'

Add a task and upload some data files with it. Files will be placed at the root of the directory where the task runs.

    # Send task as a form field named data
    curl -X POST localhost:8773/task/ -F data='{"type": "python_hello", "environment": {"frogs": 5, "CATS": 2}}' -F blanket.json=@blanket.json

    # Send task data as another file named "data"
    echo '{"type": "python_hello", "environment": {"frogs": 5, "CATS": 2}}' > data.json
    curl -X POST localhost:8773/task/ -F data=@data.json -F blanket.json=@blanket.json

Add lots of tasks

    while true; curl -X POST localhost:8773/task/ -F data=@data.json -F blanket.json=@blanket.json; echo "$(date)"; sleep 5; done

Delete a task

    curl -s -X DELETE localhost:8773/task/b200f6de-0453-46c9-9c70-5dad63db3ebb | python -mjson.tool    
    # OR
    ./blanket ps -q | tail -n5 | xargs -I {} ./blanket rm {}

    # Remove all
    ./blanket ps -a -q | xargs -I {} ./blanket rm {}

Run worker with certain capabilities

    ./blanket worker -t unix,bash,python,python2,python27

Running tests

    # For just a module
    # http://crosbymichael.com/go-helpful-commands.html
    go test ./tasks

    # For everything
    go test github.com/turtlemonvh/blanket/...


## API

```
blanket server
```

- launches server
- option to run in unix socket instead of 0.0.0.0 or 127.0.0.1 for security

```
blanket worker -t <tags> -n <number>
```

- launches n workers with capabilities defined by (optional) comma separated tags
- also command to kill workers

```
blanket task run <task> -d{<data>}
```

- run a task of the specified type, passing in a config file to parameterize
- returns a task id that can be used to track status, just like `docker run -d`
- '-d' works just like curl option; can be a file or inline json
- does basic data processing at this step so it can tell whether required fields are missing

```
blanket task stop <task> <task_id>
```

- like `docker stop`

```
blanket ps
```

- list tasks that are running, are queued to run, or have recently run + some basic stats (like top or docker ps)
- `-q` to list just ids
- change over to `blanket ls`
    - blanket ls tasks
        - completed, by tag
        - highlight tasks with no workers available to process them
    - blanket ls tasktypes
        - still read only here
    - blanket ls workers
        - list by capability

```
blanket rm
```

- FIXME: change to
    - blanket rm task <id>
    - blanket rm worker <id>




## To Do

### Short List

> See: https://trello.com/b/bOWTSxbO/blanket-dev

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


- check that required env variables are set in HTTP api and not just in UI


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

- Clean up ls/ps commands
    - list types, tasks, workers
    - allow filtering, templating
    - a lot like docker here

- set environment variables in task to make working within blanket easier
    - BLANKET_APP_TASK_ID
    - BLANKET_APP_RESULTS_DIRECTORY
    - BLANKET_APP_TASK_RESULTS_DIRECTORY
    - BLANKET_APP_WORKER_PID
    - BLANKET_APP_SERVER_PORT
- allow filling in files as templates
    - have glob patterns to match templates (relative to where they will be copied into)
- return # tasks found in response to query
    - don't put this in ordinary requests so we don't slow those down
        - /task/count/
            - everything else (options, etc) is the same
            - most of the code can be shared between these 2 endpoints
    - if >500, just say >500
        - can provide a "limit" on this too to describe the number we stop at
    - pagination on HTML interface
        - http://getbootstrap.com/components/#pagination
- package HTML into a single binary
    - maybe make this a plugin
        - https://www.elastic.co/guide/en/elasticsearch/plugins/2.2/index.html
        - https://www.elastic.co/guide/en/elasticsearch/plugins/2.2/management.html
    - plugins that implement a UI report
        - what URLs map to them
            - e.g. "_plugin/ui"
        - have all http requests at that path forwarded to them
    - UI gets packaged into a separate single binary
        - https://github.com/jteeuwen/go-bindata
        - https://github.com/elazarl/go-bindata-assetfs
        - need to add instructions
            - build js (gulp build)
            - run bindata-assets command
            - then build
- make some good examples
- put api calls into sub directories
- add ability to archive tasks and load from archive
    - archive is basically results directory + a `.blanket-task` file that defines the JSON for that task
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


### Bugs

- fix auto-refresh closing "add new" forms


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
- look into making it a service
    - https://github.com/kardianos/service

#### Testing

Set up automated tests
- https://travis-ci.org/
- https://www.appveyor.com/
    - windows CI
    - https://blog.klauspost.com/travisappveyor-ci-script-for-go/
- https://coveralls.io/
    - https://github.com/mattn/goveralls
- add docker container for running tests so users can test in isolated installation

- fs abstraction
    - https://github.com/spf13/afero

https://www.youtube.com/watch?v=ndmB0bj7eyw
- http tests
- testing process behavior

http://talks.golang.org/2012/10things.slide#8
- testing fs

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
- Add-in for check_mk local checks
    - listens for status information and writes to a file
    - may want to just use python stuff that I already made

#### Task Discovery

- allow the user to names more places to look for tasks
- look for anything that matches a certain pattern and keep a reference to it in the database as a available task type
- maybe ~/.blanket/task_types/
    - that could be a good place for configuration
    - need to define something that makes sense on lots of OSes, even ones where people have limited access

#### Design

- use card bordered areas to give everything a cleaner more organized look
- like Ionic, use divs with a slight shadow that separates them from other content
- make it flannel colors (dark, grays) instead of so bright
    - like a blanket
    - solarized dark would be good

## Specs

### MVP

- use to run ansible tasks
- use to queue tasks for OCR app
- provide log of past runs, and queue for upcoming runs
- keep all log files
- allow lots of parameters
- allow launching via http, basic ui, or command line


### Task Execution workflow

- user sends POST request to /tasks/<task type>/
- creates an item in the database with a UUID, and returns UUID to user
    - add_time is recorded in database
- adds task to queue(s)
- add extra config options for env vars
    - pass validation regex
    - pass a small list of types
- task is picked out of queue
- hash of config directory for task type is taken
    - new record for that task type is added to db
    - can exclude files with a .blanketignore file
- context variables are merged with default env
    - some default ones are added for the task
        - BLANKET_WORKING_DIR
- templates are evaluated and put in current directory, with same relative paths
    - everything is assumed to be a template unless explicitly excluded in a .templateignore file
- task execution starts
    - start_time is recorded in database
- when task completes
    - status is evaluated based on status code by default
    - recorded on database


### Important details

> This is how we want it to work, not necessarily how it works now.

* The task config is not locked when the task is added, but when it is executed
    * if you change the input files in the time between when a task is added and when it is executed, you will execute the new version of the task
* TOML files drive all configuration for tasks
    * We'll probably eventually have a web ui for drafting these
    * In the short term, we'll have a lot of examples and tests
* Most components should be pluggable
    * The default installation will be super simple, but we want to make it very easy to customize
    * Some customization
        * Task types, database driver, queue driver
    * We will probably do this by defining types in msgpack files and allowing anything in any language to evaluate and pass items back and forth
* The web UI is optional, and not included by default
    * We include an easy to use json/rest interface
    * We will probably include a simple curses interface too
    * Enabling the web UI is just 1 command to grab the files, or you can place them in the required directory
* It works well with other queuing systems
    * You can use it to distribute tasks over a TORQUE queue or similar
* It's easy to get your data out
    * It's just a bunch of json and a few tar.gzs for the directory contents


## Extra

- clean up About page
    - link to docs
    - link to twitter, github
    - version information
- security
    - SSL
        - https://godoc.org/github.com/gin-gonic/gin#Engine.RunTLS
    - allow running on unix domain docker too
        - this would make it inaccessible off the user's machine
    - HTTP basic auth
        - https://godoc.org/github.com/gin-gonic/gin#BasicAuth
            - don't store username and password directly
            - store hashed password and compare that
        - https://godoc.org/golang.org/x/crypto/bcrypt
        - allow them to do username/password (basic auth) or generate API keys (X-BLANKET-API-KEY)
            - https://github.com/MaaSiveNet/maasive-py/blob/master/maasivepy/maasivepy.py#L243
        - https://github.com/gin-gonic/gin/blob/master/auth.go#L40
            - would be pretty easy to do a version of this that stores users in bolt and uses bcrypt for lookup
    - could keep session cookies in RAM
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
- other UI
    - add ability to shut down / restart main server from web ui
    - allow user to view and edit server configuration on UI
        - may need to allow them to trigger a restart
    - allow user to back up database from UI
    - add primitive main dashboard with recent activity
    - add ability to add new task types
    - allow editing task types on HTTP interface
    - add confirmations for delete / stop commands
    - bulk delete
    - a way to show messages for things we have done (toast messages)
    - re-run a task that has run, or is stalled
    - fix memory leak (was >500 mb when running for a while)
- Option to leave task creation request open until task completes
    - would make testing easier
    - also adds it with super high priority so it is picked up fast
    - like this: https://github.com/celery/celery/issues/2275#issuecomment-56828471
- allow progress by writing to a .progress file (a single integer followed by an arbitrary string) in addition to curl
- godeps to lock in dependencies and avoid weird changes
- monitoring
    - https://github.com/shirou/gopsutil
    - total CPU and memory usage of everything
    - total disk space of all results
- better browsing interface for files
    - instead of just a list, include modified time, size, etc.
- user accounts, http auth login, account types
    - https://github.com/xyproto/permissionbolt
        - uses boltdb for simple security
    - https://github.com/tonnerre/go-ldap
        - LDAP
- cross compiling
    - should be able to combile for centos, ubuntu, windows, mac in 32bit/64bit versions all at once
    - http://dave.cheney.net/2015/08/22/cross-compilation-with-go-1-5
- command line completion
    - cobra has this built in, but will probably have to work with build system / makfile to get this right
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
    - ** this is an ideal candidate for plugin design
- autoscheduling of backups
    - list interval in config
    - when starting up, check if newest backup is too old, if so snapshot immediately
    - schedule next snapshot immediately after running first one
- plugin system
    - https://github.com/natefinch/pie
    - this system allows plugins in any languages with only serialization overhead
    - maybe using zippy (or other fast compression lib) to compress would be good?
        - can be optional
    - also messagepack to make going between languages easier
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
- pagination of results
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
    - menubar application
        - https://github.com/maxogden/menubar
        - could package this into distributions on windows and mac


## Similar Projects

- check out iron.io for data model and UI
    - http://www.iron.io/
    - https://www.google.com/search?q=iron.io+dashboard&source=lnms&tbm=isch
- more control over workers
    - maybe they listen on a port too?
    - http://flower.readthedocs.org/en/latest/api.html
    - https://github.com/ajvb/kala#overview-of-routes
- https://github.com/albrow/jobs
    - A persistent and flexible background jobs library for go.
- https://github.com/ajvb/kala
    - similar
    - time based task scheduler
- https://github.com/mesos/chronos
    - executes sh scripts
- http://docs.celeryproject.org/en/latest/reference/
    - celery api
- https://github.com/RichardKnop/machinery
    - celery replica in golang
- https://github.com/airbnb/airflow
    - task management, scheduling, dependencies
- https://github.com/glycerine/goq
    - sungridengine replica in golang with encryption
- https://github.com/hammerlab/ketrew
    - workflow engine able to run arbitrary tasks
    - plus UI

