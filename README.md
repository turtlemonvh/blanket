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

Delete a task

    curl -s -X DELETE localhost:8773/task/b200f6de-0453-46c9-9c70-5dad63db3ebb | python -mjson.tool    
    # OR
    ./blanket ps -q | tail -n5 | xargs -I {} ./blanket rm {}

    # Remove all
    ./blanket ps -a -q | xargs -I {} ./blanket rm {}

Run worker with certain capabilities

    ./blanket worker -t unix,bash,python,python2,python27

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
- `-q` to list just ids
- change over to `blanket ls`
    - blanket ls tasks
        - completed, by tag
        - highlight tasks with no workers available to process them
    - blanket ls tasktypes
        - still read only here
    - blanket ls workers
        - list by capability

blanket rm

- change to
    - blanket rm task <id>
    - blanket rm worker <id>


>>> Others

- list tasks
- list running jobs
- show curses interface for monitoring progress (top)
- list task types
- add/remove task


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


## To Do

Short List

- Allow configurable executors
    - copy command into tmp file; run with bash, zsh, bat
    - make it use a different executor depending on OS (put this in viper)
    - e.g. http://ss64.com/nt/syntax-run.html
    - http://stackoverflow.com/questions/4571244/creating-a-bat-file-for-python-script
- allow filling in files as templates
    - have glob patterns to match templates (relative to where they will be copied into)
- get rid of name field for task types and derive from file name
    - this makes it easier to track
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
- make it work with windows
- add a type field to the TOML files
    - bash, docker, python
- allow progress by writing to a .progress file (a single integer) in addition to curl
- launch new workers over http
- view workers via ps
    - keep list of workers in database so have reference to pids
- stream logfiles
    - also allow the user to view streaming stdout/stderr
        - http://kvz.io/blog/2013/07/12/prefix-streaming-stdout-and-stderr-in-golang/
- controlling running tasks
    - stop / restart
- clean up logging to be more consistent
    - make it configurable in terms of verbosity

Look over

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

- task is picked out of queue
- worker wrapper process is forked and started in a new directory
    - /opt/blanket/scratch/task/<task type>/<task id>
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



Task States


Things defined in task config file

- logfiles or stdout/stderr for process
- default environment variables
- bash command as a string
- any checks to perform in the environment
    - e.g. make sure an executable exists, is at a given value; credential files exist
    - these are just bash scripts too
- files to ignore when
    - taking directory hash (versioning) : .blanketignore
    - evaluating files as templates
    - copying input files to scratch directory
- any input files posted
    - we encourage the user to use substitution to fill in templates, but can accept any type of file as part of the input


- base task is a bash task
    - each task has its own TOML file
    - TOML files can point to multiple directories
        - each directory section can specify options for what files to copy over
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


Workers

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
- launch as daemon
    - https://groups.google.com/forum/#!topic/golang-nuts/shST-SDqIp4
    - see Evernote notebook: https://www.evernote.com/shard/s98/nl/2147483647/d1949bb5-cdf1-4422-8773-862276c6bd36/

Helper scripts

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
- check out iron.io for data model and UI
    - http://www.iron.io/
    - https://www.google.com/search?q=iron.io+dashboard&source=lnms&tbm=isch

## REST API

- POST new task of a given type
    - get back id
- GET tasks
    - with tags
        - find the highest priority task based off fair scheduling
    - want to show list of upcoming based on workers available
        - also show if any have no qualified workers

workers
- e.g. 
    - http://flower.readthedocs.org/en/latest/api.html
    - https://github.com/ajvb/kala#overview-of-routes
- GET
- DELETE (stop)
- POST (add a new one with certain tags)


- task type options
- option to upload files as part of task execution
    - params
        - env variables; also available in templates
        - files
        - settings (override values on a per task basis)


- Task states; move to a different bucket
    - QUEUED
    - STARTING
    - RUNNING
    - SUCCESS/ERROR
- option to requeue
- mark each task with a TTL and a start time
    - if not finished, retry according to retry policy


## Extra


- include weights for fair queuing
- template extensions
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
- pluggable queue / datastore
    - use amazon RDS (as queue and datastore) to start to make distributed large deployments easy
        - RDS would maintain information about instances connected, workers available
        - using RDS and https://golang.org/pkg/database/sql/ means any sql database is ok
    - each type just has to implement a certain interface
        - for most, send string, get back string
        - input string is request json, output string is response json
- pluggable result store
    - allow writing to a tmp directory and then storing on s3
- use render to output templates content
    - https://github.com/unrolled/render#gin
- Cookie cutter / quickstart that generates an example project for people to get started developing wrapper in various languages with docs included
- Scheduling / future execution
    - should be able to set min start date for a task
    - periodic scheduling would be good too, though this is pretty easy with cron, so not a big deal
- Mark tasks so that only 1 version of the task can be running at a time
- Task dependencies
    - similar to airflow and bamboo
- add `?v` argument to provide pretty printed output
- allow TOML file inheritance, starting with a different base task type
    - https://github.com/spf13/viper/blob/master/viper.go#L938
    - to start, everything is bash
- fs abstraction
    - https://github.com/spf13/afero
- pagination of results
- moving tasks in different states to different buckets
    - would make scanning to find new tasks faster if all ERROR/SUCCESS tasks weren't in the same place
- make some good examples
- write some tests
- set up hugo to generate api docs
    - https://gohugo.io/overview/introduction/
    - render into a single page
        - https://gohugo.io/extras/toc/
- recommended tool for making your thing available
    - https://ngrok.com/
- allow uploading created files to s3
    - this should probably just be part of the task, but uploading and then clearing the directory would be a common cool task
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

