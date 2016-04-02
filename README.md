# Blanket

> WARNING: Work in progress. Useful but still rough.

A RESTy wrapper for services.

## Quick Start

    go build .
    ./blanket -h

View tasks

    curl -s -X GET localhost:8773/task/ | python -mjson.tool
    # OR
    ./blanket ps

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

    while true; do curl -X POST localhost:8773/task/ -F data=@data.json -F blanket.json=@blanket.json; echo "$(date)"; sleep 5; done

Delete a task

    curl -s -X DELETE localhost:8773/task/b200f6de-0453-46c9-9c70-5dad63db3ebb | python -mjson.tool    
    # OR
    ./blanket ps -q | tail -n1 | xargs ./blanket rm

    # Remove all
    ./blanket ps -q | xargs -I {} ./blanket rm {}

Run worker with certain capabilities

    # You can also launch from the web UI or via an api call
    ./blanket worker -t unix,bash,python,python2,python27

Running tests

    # For just a module
    go test ./tasks

    # For everything
    go test ./...


## Command line API

```
blanket
```

- runs server

```
blanket worker -t <tags> -n <number>
```

- launches n workers with capabilities defined by (optional) comma separated tags
- also command to kill workers

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

- remove a single task
- FIXME: change to
    - blanket rm task <id>
    - blanket rm worker <id>


## Specs

### Implementation Details

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
- https://github.com/victorcoder/dkron
    - distributed task scheduling system
    - http://dkron.io/
    - checks in all its js components
    - uses serf for membership
    - uses tags to allocate jobs
    - has configurable backend for storage
        - etcd, zookeeper, consul
    - notifications through email are built in
        - maybe not so configurable/pluggable
        - can use webhooks for notifications
- https://github.com/kandoo/beehive
    - https://github.com/kandoo/beehive/tree/master/examples/taskq
    - example distributed task queue and task processing system
    - more like MPI
- https://queue.acm.org/detail.cfm?id=2745840
    - article descibing the design of Google's cron system
    - mentions anacron, for running jobs that *would* have run if the system had not been down
    - some tasks are safe to re-run, some are safe to skip, some are neither
    - they recommend job owners monitoring job state, and having cron itself be simpler
    - determining a failed job or machine (via health checks) takes time, and can change the time at which tasks run
    - cron jobs should specify their resource needs, and be limited to those resources when running
        - can kill jobs that go over resource limits
    - 2 options for tracking state
        - 1: use existing data store
        - 2: store data in cron itself
        - they say 2 is better bc ditributed file systems are bad for small writes and cron should have few dependencies
            - I am not super sold on this
    - in their implementation, failure of master is detected in seconds
        - master election protocol is specific to cron service
    - master must stop scheduling actions as soon as it is no longer master
        - as soon as it can't heartbeat and confirm success of heartbeat, it needs to stop
        - can't assume it is master
    - master and slaves must all have a consistent view on list of tasks and schedule
    - having deterministic ids for tasks makes it easy to check the state of a specific instance of a task from anywhere
        - though it you store in something more complex than a k/v store, getting this is not too tough...
    - keeps a finite amt of state
    - permits "?" in cron tab, indicating any (single) value is permissable
        - e.g. needs to run every 24 hours, but I don't care exactly when
        - they just hash the job config over the time range to pick a value
    - IDEAS
        - add "checkpoints" people can define, when before the check point no cleanup has to be done
        - checkpoints can also trigger a upload of state to distributed file system
        - add "cleanup" for partial tasks
        - default behavior on failure should be to not retry
        - re-schedule jobs every time configuration loads
        - keep a limited amt of history, configured per job
            - archive anything older
        - handling heartbeats with 1000s of servers may be tough
            - 1000 servers all checking every 5 seconds = 200 writes per second JUST FOR HEARTBEAT
        - allow scheduling for every machine in the cluster
            - this schedules N jobs, each with a tag for a specific machine
- https://github.com/drone/drone
    - jenkins replacement
    - executes everything in docker containers

