# API

## Tasks

Called by users. Used to manage task flow.

```
GET /task/
GET /task/:taskId
POST /task/
DELETE /task/:taskId
GET /task/:taskId/log
PUT /task/:taskId/cancel
```

Called by workers.  Used to communicate task progress.

```
POST /task/claim/:workerId
PUT /task/:id/run
PUT /task/:id/finish
PUT /task/:id/progress
```

## Task Types

```
GET /task_type/
GET /task_type/:name

```

## Workers

Get infomation about workers.

```
GET /worker/
GET /worker/:id
GET /worker/:id/logs
```

Controlling the behavior of workers.

```
POST /worker/
PUT /worker/:id
PUT /worker/:id/stop
PUT /worker/:id/restart
DELETE /worker/:id
```
