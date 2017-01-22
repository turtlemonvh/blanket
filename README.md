# Blanket

A RESTy wrapper for services.

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

Once the server is running, you can view the web ui at http://localhost:8773/.  You can also interact with tasks

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



