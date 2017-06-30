# Blanket

Blanket is a RESTy wrapper for long running tasks.

## Quick Start

```bash
# Or windows, or linux
make darwin

# Create a config file
cat > blanket.json << EOF
{
  "port": 8773,
  "tasks": {
    "typesPaths": ["./types"],
    "resultsPath": "results"
  },
  "logLevel": "debug"
}
EOF

# Run server
./blanket
```

Once the server is running, you can view the web ui at [http://localhost:8773/](http://localhost:8773/).  You can also interact with blanket via curl and the command line.  For example, you can list tasks

```bash
curl -s -X GET localhost:8773/task/ | jq .
# OR
./blanket ps
```

Add a new task

```bash
# Add 3 different tasks with different params
curl -s -X POST localhost:8773/task/ -d'{"type": "bash_task", "environment": {"PANDAS": "four", "Frogs": 5, "CATS": 2}}'
curl -X POST localhost:8773/task/ -d'{"type": "bash_task", "environment": {"PANDAS": "four", "Frogs": 5, "CATS": 2, "DEFAULT_COMMAND": "cd ~ && ls -lah"}}'
curl -X POST localhost:8773/task/ -d'{"type": "python_hello", "environment": {"frogs": 5, "CATS": 2}}'
```

Add a task and upload some data files with it. Files will be placed at the root of the directory where the task runs.

```bash
# Send task as a form field named data with multiple files
curl -X POST localhost:8773/task/ -F data='{"type": "python_hello", "environment": {"frogs": 5, "CATS": 2}}' -F blanket.json=@blanket.json

# Send task data as another file named "data"
cat > data.json << EOF
{
    "type": "python_hello", 
    "environment": {
        "frogs": 5, 
        "CATS": 2
    }
}
EOF
curl -X POST localhost:8773/task/ -F data=@data.json -F blanket.json=@blanket.json
```

Add lots of tasks

```bash
while true; do 
    curl -X POST localhost:8773/task/ \
        -F data=@data.json \
        -F blanket.json=@blanket.json; 
    echo "$(date)"; 
    sleep 5; 
done
```

There is also limited functionality for sending tasks via the command line.

```
# Send in a single task
./blanket submit -t python_hello -e '{"frogs": 5}'
python_hello 5955c9e26b3c65257abc1e32 [1498794466]

# Send in a single task, printing only the id of the task once it is submitted
./blanket submit -t python_hello -e '{"frogs": 5}' -q
5955c9df6b3c65257abc1e31
```

Delete a task

```bash
curl -s -X DELETE localhost:8773/task/b200f6de-0453-46c9-9c70-5dad63db3ebb | jq . 
# OR
./blanket ps -q | tail -n1 | xargs ./blanket rm

# Remove all tasks
./blanket ps -q | xargs -I {} ./blanket rm {}
```

Run worker with certain capabilities

```bash
# You can also launch from the web UI or via an api call
./blanket worker -t unix,bash,python,python2,python27
```

## Command line API

```bash
$ ./blanket-darwin-amd64 -h
A fast and easy way to wrap applications and make them available via nice clean REST interfaces with built in UI, command line tools, and queuing, all in a single binary!

Usage:
  blanket [flags]
  blanket [command]

Available Commands:
  ps          List active and queued tasks
  rm          Remove tasks
  version     Print the version number of blanket
  worker      Run a worker with capabilities defined by tags

Flags:
  -c, --config string     config file (default is blanket.yaml|json|toml)
      --logLevel string   the logging level to use (default "info")
  -p, --port int32        Port the server will run on (default 8773)

Use "blanket [command] --help" for more information about a command.
```

## Specs

See [the docs directory](https://github.com/turtlemonvh/blanket-api/tree/master/docs) for more detailed information about the design of blanket.

### Origin

Blanket was designed because of problems I saw on several projects in [GTRI's ELSYS branch](https://www.gtri.gatech.edu/elsys) where we wanted to be able to integrate a piece of software with a less than awesome API into another tool.  Whether that software was a long running simulation, a CAD renderer, or some other strange beast, we kept seeing people try to wrap HTTP servers around these utilities.  This seemed unnecessary and wasteful.

The starting concept of blanket was simple: If we can wrap anything with a command line call (which is possible with tools like [sikuli](http://www.sikuli.org/)), and we could make it easy to expose any command line script as a web endpoint, then we can provide a nice consistent way to expose cool software with a possibly bad API to a larger class of users.

The first draft of blanket was written in python and used celery for queuing. It worked fine, but was a bit heavy weight, and was hard for some Windows users to install. Go was chosen for the rewrite since

* It compiles to a single binary, so deployment is easy
* It cross compiles to many platforms, so getting it to behave on windows shouldn't be too painful

This was my first major project in Go, and the code base is still recovering from some early "experiments".  It is still a bit rough around the edges, but I do use blanket almost every day at work to manage long running tasks that I'd like to keep a record of, like code deployments.  In this regard, it's a bit like a light-weight [jenkins](https://jenkins.io/).  I plan to continue working on blanket, and I expect it will become a major component of many of my future side projects.

If you are interested in using blanket for a project and want to ask whether blanket may be a good match, you can either [submit a github issue with your question](https://github.com/turtlemonvh/blanket-api/issues) or find me on twitter [@turtlemonvh](https://twitter.com/turtlemonvh).

### Design Goals

> This is how we want it to work, not necessarily how it works now.

* Speed is not a high priority at the moment. Instead, we favor 
    * simplicity: API is easy to work with, and tasks are hard to lose
    * pluggability: It is easy to change storage and queue backends while maintaining the same API.
    * traceable: It's easy to understand what's going on.
    * open: It's easy to get data in and out.
    * low resourse usage: Like [xinetd](https://en.wikipedia.org/wiki/Xinetd), it can be present and usable without you thinking about it.
* Blanket is designed for long running tasks, not high speed messaging. We assume
    * Tasks will be running for a long time (several seconds or more).
    * Contention between workers will be fairly low.
* TOML files drive all configuration for tasks
* The web UI is optional
    * Everything can be done without it, easily
    * The main feature is a json/rest interface


