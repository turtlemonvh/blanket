# Task Type Definitions

> This page is a stub and will be filled in with more examples.

Task definitions are constructed using [toml](https://npf.io/2014/08/intro-to-toml/).  The only requirements are:

* the filename must end in toml
* the file must be in one of the locations listed in the `tasks.typesPaths` variable in the server config
* the `command` field is present (it is currently the only required field)

The command can use [go templates](https://golang.org/pkg/text/template/) to variables defined in the environment.

## Field names

### tags

Tags is a list of strings.  It defines what capabilities are required of workers that want to execute this task.

### timeout

Timeout is the max duration of the task in seconds.

### command

The command you want to execute when the task runs.

### executor

This defines the executor type that can be used to interpret your command.  Currently bash is the only option.

### environment

A map of environment variables with 3 sections: default, required, and optional.

* default: these env variables will be present by default, but can be overridden
* required: these env variables must be sent when a new task instance is created
* optional: these env variables can be set but are not required and may not have a default value (mostly available for documentation and discoverability)

All of these take a name and description field.  `default` values also take a `value` field.  When calling a task, you can always add additional env variables that are not part of the task definition.

Since environment variables are the main unit of configurability for tasks, this is where most of the complexity is.

## Examples

### A simple bash task that runs a user supplied command

```toml
tags = ["bash", "unix"]

# timeout in seconds
timeout = 200

# The command to execute
command='''
{{.DEFAULT_COMMAND}}
'''

executor="bash"

    # Environment variables are injected into the process environment

    [[environment.default]]
    name = "ANIMAL"
    value = "giraffe"

    [[environment.default]]
    name = "SECOND_ANIMAL"
    value = "hippo"

    # Remember, everything is interpreted as a string when passed as an env variable
    [[environment.default]]
    name = "NUM_FROGS"
    value = "3"

    [[environment.required]]
    name = "DEFAULT_COMMAND"
    description = "The bash command to run. E.g. `echo $(date)`"
```