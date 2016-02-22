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


- fix auto-refresh closing "add new" forms
- show task type description when launching a new task



- HTTP interface
    - add confirmations for delete / stop commands
    - bulk delete
    - a way to show messages for things we have done (toast messages)
    - re-run a task that has run, or is stalled
    - launch and manage workers
    - figure out bug where form closes sometimes when clicking "Remove"
    - fix memory leak (was >500 mb when running for a while)
- Allow configurable executors
    - copy command into tmp file; run with bash, zsh, bat
    - make it use a different executor depending on OS (put this in viper)
    - test on windows
    - e.g. http://ss64.com/nt/syntax-run.html
    - http://stackoverflow.com/questions/4571244/creating-a-bat-file-for-python-script
- Clean up ls/ps commands
    - column alignment
- allow filling in files as templates
    - have glob patterns to match templates (relative to where they will be copied into)
- Make use of timeout to stop long running tasks
    - also restart and cleanup
    - If is task has passed its run time, unlock it and return to queue
- allow user to use a previous task as a template for a new task
- controlling running tasks
    - stop / restart
    - configurable # restarts
        - # times allowed, whether they go in new directories
- return # tasks found in response to query
    - make this an options request; we don't want this to slow down ordinary requests
    - if >500, just say >500
    - pagination on HTML interface
        - http://getbootstrap.com/components/#pagination
- package HTML into a single binary
    - https://github.com/jteeuwen/go-bindata
    - https://github.com/elazarl/go-bindata-assetfs
    - need to add instructions
        - build js (gulp build)
        - run bindata-assets command
        - then build
- make names of fields on objects consistent
    - esp. task types vs tasks
- allow user to view and edit server configuration on UI
    - may need to allow them to trigger a restart
- allow user to back up database from UI
- workers
    - assign a unique id to make tracking logfiles for past workers easier
    - name logfile based on time of day
    - allow pausing
    - view worker status via ps / ls
        - keep list of workers in database so have reference to pids
    - send worker logs to stdout addition to sending to a file
        - logrus makes this pretty simple: https://github.com/Sirupsen/logrus
- make some good examples
- put api calls into sub directories
- allow it to run without a configuration file
    - make sure it has sane defaults
    - can add an installer later (esp. for windows) or package (for linux) that sets up config for you later



### Larger Efforts

#### Testing

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

#### Documentation

- set up hugo to generate api docs
    - https://gohugo.io/overview/introduction/
    - render into a single page
        - https://gohugo.io/extras/toc/

#### Log cleanup

- stream logfiles (for viewing in browser); both worker and process logs
    - both last few hundred lines in a stream and full download
    - https://godoc.org/github.com/hpcloud/tail
    - http://stackoverflow.com/questions/19292113/not-buffered-http-responsewritter-in-golang
    - https://github.com/julienschmidt/sse
    - https://godoc.org/github.com/julienschmidt/sse#Streamer.SendString
    - check how supervisord web does it
        - https://github.com/Supervisor/supervisor/blob/master/supervisor/ui/tail.html
        - https://github.com/Supervisor/supervisor/blob/master/supervisor/http.py#L140
    - python
        - https://gist.github.com/fiorix/1920022
        - http://stackoverflow.com/questions/25166770/angularjs-with-server-sent-events
- configurable logging verbosity
    - these are things that every task must provide, or it will be rejected
- log to multiple locations
    - https://github.com/Sirupsen/logrus
    - https://godoc.org/github.com/Sirupsen/logrus#Logger
    - https://golang.org/pkg/io/#MultiWriter
    - just create your own logging package that takes a config and writes to multiple locations at different verbosities
        - configure as package global objects for easy import
        - https://github.com/Sirupsen/logrus#rotation
        - https://github.com/natefinch/lumberjack
            - package for rotation
- better worker logs with SSE and json logs
    - can include little event cards for everything that happened, even highlight errors, provide search and filtering, aggregate events
- allow user to specify where worker logs go (directory)


#### Task Discovery

- allow the user to names more places to look for tasks
- look for anything that matches a certain pattern and keep a reference to it in the database as a available task type

#### Design

- use card bordered areas to give everything a cleaner more organized look
- like Ionic, use divs with a slight shadow that separates them from other content
- make it flannel colors (dark, grays) instead of so bright
    - like a blanket
    - solarized dark would be good



MVP

- use to run ansible tasks
- use to queue tasks for OCR app
- provide log of past runs, and queue for upcoming runs
- keep all log files
- allow lots of parameters
- allow launching via http, basic ui, or command line


Task Execution workflow

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


Important details

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

- security
    - SSL
    - HTTP basic auth
- other UI
    - add primitive main dashboard with recent activity
    - add ability to reconfigure and restart
    - add ability to add new task types
    - allow editing task types on HTTP interface
- Option to leave task creation request open until task completes
    - also adds it with super high priority so it is picked up fast
    - like this: https://github.com/celery/celery/issues/2275#issuecomment-56828471
- allow progress by writing to a .progress file (a single integer followed by an arbitrary string) in addition to curl
- pulse "running" tag for running tasks to make them more obvious
- godeps to lock in dependencies and avoid weird changes
- option to run with tls
- monitoring
    - https://github.com/shirou/gopsutil
    - total CPU and memory usage of everything
    - total disk space of all results
- option to do a clean reload
    - https://github.com/fvbock/endless
    - https://github.com/rcrowley/goagain
- better browsing interface for files
    - instead of just a list, include modified time, size, etc.
- Stats
    - put stats into their own bucket
    - each blob is its own packet of stats for a time window
    - scanning through this bucket we can quickly pull out the stats we need and make a plot
    - stat viz: http://www.tnoda.com/blog/2013-12-19, http://cmaurer.github.io/angularjs-nvd3-directives/sparkline.chart.html
- Add prometheus and expvar metrics
    - see logstore as an example
- Add-in for check_mk local checks
    - listens for status information and writes to a file
    - may want to just use python stuff that I already made
- Clean up formatting of ps command
    - https://golang.org/pkg/text/tabwriter/
    - https://github.com/olekukonko/tablewriter
    - https://socketloop.com/references/golang-text-tabwriter-newwriter-function-and-write-method-example
    - https://github.com/docker/docker/blob/master/api/client/ps.go
    - https://github.com/docker/docker/blob/master/api/client/formatter/formatter.go
        - uses tabwriter
- put results into directories with task name as top directory
    - allow the directory structure underneath to be configurable
        - e.g. dates, just ids, etc
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
- use render to output templates content in responses
    - https://github.com/unrolled/render#gin
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
- add `?v` argument to provide pretty printed output
- allow TOML file inheritance, starting with a different base task type
    - https://github.com/spf13/viper/blob/master/viper.go#L938
    - to start, everything is bash
- pagination of results
- moving tasks in different states to different buckets
    - would make scanning to find new tasks faster if all ERROR/SUCCESS tasks weren't in the same place
- recommended tool for making your thing available
    - https://ngrok.com/
- allow uploading created files to s3
    - this should probably just be part of the task, but uploading and then clearing the directory would be a common cool task
- fs abstraction
    - https://github.com/spf13/afero
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

## Benchmarking

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
- https://github.com/glycerine/goq
    - sungridengine replica in golang with encryption
- https://github.com/hammerlab/ketrew
    - workflow engine able to run arbitrary tasks
    - plus UI

