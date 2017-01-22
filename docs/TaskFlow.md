# Task Flow

This page documents the workflow that tasks go through.

## Definitions

Task states

| State | Description |
| ------ | ------- |
| WAITING | Task has been posted but is not being worked on.  The task is in the queue. |
| CLAIMED | A worker has requested this task.  The task is now out of the queue and state is maintained only on the database. |
| RUNNING | The worker has executed the task preconditions (such as grabbing task type state, copying files) and the main command is now running. The worker may send additional updates during this state. |
| ERROR | The worker has encountered an error while running the task. Any non 0 status code is interpreted as an error. |
| SUCCESS | The worker has finished task execution. |
| STOPPED | The worker has received a command to stop execution of this task and the command has been killed. |
| TIMEDOUT | The task took longer than the allowed time and was killed. |

## Basic Task Flow

This is what the workflow looks like without any user intervention or failures.

### 0. Preconditions / Assumptions

We start out with the assumptions that

1. Blanket itself is running.
1. Workers are running, and they can consume tasks.

### 1. User posts task

User sends `POST /task/`.  They receive back an id for their task.
The task is put in both the database and the queue.  The write to the database is performed first.

### 2. Worker claims task

Workers do not claim specific tasks. They send a specification of their capabilities to the server via `POST /task/claim/:workerId`. The server responds to this by executing a series of actions.

1. Find a task that matches that worker's capability in the queue.
1. Insert that task into the database in the `CLAIMED` state, and ack the message from the queue.
1. Return the task id of the claimed task to the worker.

### 3. Worker begins task execution

Upon receipt of this task id from the server, the worker starts performing its own series of actions to advance the task state.

1. Grab the task type information.
1. Create an isolated execution directory for the task. Copy any template files in this directory and fill them out.
1. Start executing the main task command.
1. Send a `PUT /task/:id/run` back to the server to request the server advances the task state to `RUNNING`.

> Note that **the task config is not locked when the task is added**, but when it is executed. If you change the input files in the time between when a task is added and when it is executed, you will execute the new version of the task. This may change in the future.

During execution, the worker may send multiple requests to `PUT /task/:id/progress` to update the percent completion of the task, or adjust other task attributes.

### 4. Worker completes task execution

Assuming the task execution completes without any errors, timing out, or being stopped by the user, the worker will then

1. Send a request to `PUT /task/:id/finish` to mark the task as complete.
1. Ask the server for another task.

