# Blanket

A RESTy wrapper for services.

## Running

    go build .
    ./blanket -h

Running tests

    # For just a module
    # http://crosbymichael.com/go-helpful-commands.html
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

- launches n workers with capabilities defined by (optional) comma separated tags
- also command to kill workers

blanket task run <task> -d{<data>}

- run a task of the specified type, passing in a config file to parameterize
- returns a task id that can be used to track status, just like `docker run -d`
- '-d' works just like curl option; can be a file or inline json
- does basic data processing at this step so it can tell whether required fields are missing

blanket task stop <task> <task_id>

- like `docker stop`

blanket ps

- list tasks that are running, are queued to run, or have recently run + some basic stats (like top or docker ps)

>>> Others

- list tasks
- list running jobs
- show curses interface for monitoring progress (top)
- list task types
- add/remove task type
- add/remove job


## Data Model

- Maybe drive with a TOML file configuration that is populated by variables?
- Distinguish between types and instances
    - users should be able to add new types AND new instances of a type at runtime
- Write tests to save to database and reload from database
- Write interfaces for each type and base implementations


Queue

- each task is tagged
- database backed red-black tree
    - https://en.wikipedia.org/wiki/Priority_queue#Usual_implementation
    - http://stackoverflow.com/questions/6147242/heap-vs-binary-search-tree-bst
        - not a heap, since will need to scan the first few items to find one that can be processed
    - https://godoc.org/github.com/erriapo/redblacktree
    - https://github.com/petar/GoLLRB
        - https://github.com/HuKeping/rbtree
- a consumer asks for the highest rated item with a tag it can consume
- need a simple heap implementation on top of boltdb
    - just add an object and maintain a reference to its priority in the heap
    - use a single bucket for all tasks in the queue
    - keep the heap in memory
        - a quick rescan of all the keys rebuilds the heap
        - all you keep on disk is the (id, priority, tags)
- may want to add fair scheduling later
    - so older / longer / lower priority tasks are not starved, but shorted tasks are escalated
    - can write a custom priority algorithm and find place in heap via binary comparisons
- option to kill jobs automatically if they go over their time estimates
- schedule based on
    - priority
    - time in queue
    - job duration
- default
    - lower # points is better
    - duration
        - 1 pt per minute (min 1)
    - weight
        - divide duration in minutes by X (min 1)
    - time added
        - add # minutes since the epoch when added (round down)
        - so eventually very old tasks will be worth more
        - show this # minutes in queue in the UI
- https://github.com/boltdb/bolt#iterating-over-keys
    - make the key the priority so scanning over items is super fast and it is stored in sorted order
    - http://stackoverflow.com/questions/16888357/convert-an-integer-to-a-byte-array
    - just want to take an integer value and use that as the key
    - adding and removing may be slow, but lots of sequential reads can happen at the same time


BaseTaskType

- name
- description
- input vars / settings
    - these may all be typed, or not
- config file
    - TOML file
- duration
    - default 1 minute
- weight
    - default 1
~~~
- steal code from here for saving items in boltdb
    - https://github.com/djherbis/stow

BaseTask

- task id
- TaskType (fk)
- % complete: 0-100
    - auto changed to 100 when complete
- input vars / settings
    - map[string]string
- times
    - time_created
    - time_queued
    - time_started
    - time_finished
- status
    - STARTING
    - QUEUED
    - RUNNING
    - FAILED/SUCCESS
- commands
    - start
    - stop/kill
    - status
    - NO: restart
- duration and weight
    - can override type values


TaskStatus

- task_id
- status
- time
> Used to track progress of task along with % complete


BashTask

- template for bash script
- input files
- output files
    - names of files can be populated by variables
    - variables include task id, date
- stdout
- stderr


PythonTask

- include a python base class for task that includes feedback
- this should go in a separate directory
    - /plugins/python/



## To Do

Look over

- https://github.com/ajvb/kala
    - similar
    - time based task scheduler

MVP

- use to run ansible tasks
- provide log of past runs, and queue for upcoming runs
- keep all log files
- allow lots of parameters
- allow launching via http, basic ui, or command line


Task Execution workflow

- user sends POST request to /tasks/<task type>/
- creates an item in the database with a UUID, and returns UUID to user
    - add_time is recorded in database
- adds task to queue(s)

- task is picked out of queue
- worker wrapper process is forked and started in a new directory
    - /opt/blanket/scratch/task/<task type>/<task id>
- hash of config directory for task type is taken
    - new record for that task type is added to db
    - can exclude files with a .versionignore file
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

* The task state is not the state when the task is added, but when it is executed
    * if you change the input files in the time between when a task is added and when it is executed, you will execute the new version of the task
* TOML files drive all configuration for tasks
    * We'll probably eventually have a web ui for drafting these
    * In the short term, we'll have a lot of examples and tests
* Most components should be pluggable
    * The default installation will be super simple, but we want to make it very easy to customize
    * Some customization
        * Task types, database driver, queue driver
    * We will probably do this by defining types in msgpack files and allowing anything in any lanuage to eveluate and pass items back and forth
* The web UI is optional, and not included by default
    * We include an easy to use json/rest interface
    * We will probably include a simple curses interface too
    * Enabling the web UI is just 1 command to grab the files, or you can place them in the required directory
* It works well with other queuing systems
    * You can use it to distribute tasks over a TORQUE queue or similar
* It's easy to get your data out
    * It's just a bunch of json and a few tar.gzs for the directory contents



Task States


Things defined in task config file
- logfiles or stdout/stderr for process
- default environment variables
- bash command as a string
- any checks to perform in the environment
    - e.g. make sure an executable exists, is at a given value; credential files exist
    - these are just bash scripts too
- files to ignore when
    - taking directory hash (versioning)
    - evaluating files as templates
    - copying input files to scratch directory
- any input files posted
    - we encourage the user to use substitution to fill in templates, but can accept any type of file as part of the input


- base task is a bash task
    - each task has its own directory
    - each directory has a TOML conf file that defines the task
        - TOML file allows override of task type
    - also include python task type as a plugin
        - plugins are just directories dropped in /opt/blanket/plugins/
        - the python executor just defines a base class that has hooks for updating and other fanciness
    - also include a vbs type as a plugin
        - vbs: https://technet.microsoft.com/en-us/scriptcenter/dd940112.aspx
        - more vbs: http://www.makeuseof.com/tag/batch-windows-scripting-host-tutorial/
    - most base types should be able to perform basic actions just with a config file
    - takes a template, fills in environment variables (similar to envsubstr)
    - can override any env variables per task
    - entire task state (env) is saved in a json file + hash of directory and date of first run as a version
        - versions are saved automatically whenever a task is run if the hash has changed
        - task id information is available in env variables
    - stdout and stderr are saved in 2 files by default, can be combined
- option to isolate each task
    - changes into a scratch directory that is namespaced per task, like on bamboo
    - still allows access to OS functions
- interface is json/REST based to start
- the UI is optional; just need to download the required files into /opt/blanket/ui/
    - these are served by default if available, otherwise 404s
- option to run with `?sync` command
    - holds open the connection until task returns so you don't have to poll for task state
- for plugins
    - e.g. database driver
    - allow them to be specified in base TOML config
    - start up and communicate over stdin/stdout
    - in go (or maybe even python, since just doing stdin/stdout, not direct RPC)


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

## UI

- show a browsable file system viewer per result task
    - can go into folders, list files, download individual files, etc.
    - option to render files inline if possible
    - just need a simple js file system tree
- list tasks by state
- list tasks by priority
- list workers
- manage workers
    - stop start, add new ones
- security
    - SSL and basic auth
    - basic auth creds are in config file
    - uses Go's build in SSL capabilities to configure itself; can use your key files or make its own


## Extra

- Cookie cutter / quickstart that generates an example project for people to get started developing wrapper in various languages with docs included


