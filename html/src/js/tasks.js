angular.module('blanketApp')
    .service('TasksStore', ['$http', 'baseUrl', '$log', '$timeout', 'LocalStore', 'diffFeatures', '_', function($http, baseUrl, $log, $timeout, localStorage, diffFeatures, _) {
        var self = this;

        this.tasks = [];
        this.taskTypes = [];

        self.cleanTask = function(t) {
            var labelClasses = {
                "WAIT": "default",
                "START": "primary",
                "RUNNING": "warning",
                "ERROR": "danger",
                "SUCCESS": "success",
                "TIMEOUT": "danger",
                "STOPPED": "danger"
            };
            t.labelClass = labelClasses[t.state];
            t.hasResults = _.intersection(["WAIT", "START"], [t.state]).length === 0;
            t.isComplete = _.intersection(["WAIT", "START", "RUNNING"], [t.state]).length === 0;

            // Date fixing
            var dateFields = ['createdTs', 'startedTs', 'lastUpdatedTs'];
            _.each(dateFields, function(df) {
                t[df] = t[df] * 1000;
            });
        }

        // FIXME: handle pagination and offsets
        self.refreshTasks = function() {
            var r = $http.get(baseUrl + '/task/?limit=50&reverseSort=true').then(function(d) {
                self.tasks = d.data;
                _.each(self.tasks, function(v) {
                    self.cleanTask(v);
                })
                $log.log("Found " + self.tasks.length + " tasks")
            });

            r.then(function() {

                var df = new diffFeatures.df(function(task) {
                    return _.map(task.defaultEnv, function(v, k) {
                        return k + "=" + v;
                    })
                });

                // Build up counts, set features attribute
                _.each(self.tasks, function(task) {
                    task.allFeatures = _.sortBy(df.addItem(task));
                    task.allFeaturesList = _.join(task.allFeatures, "\n");
                })

                // Best features for each task
                _.each(self.tasks, function(task) {
                    task.bestFeatures = df.getBestOfFeatures(task.allFeatures);
                })

            })
        }

        self.refreshTaskTypes = function() {
            $http.get(baseUrl + '/task_type/?limit=50').then(function(d) {
                self.taskTypes = d.data;
                _.each(self.taskTypes, function(v) {
                    // Date fixing
                    var dateFields = ['loadedTs'];
                    _.each(dateFields, function(df) {
                        v[df] = v[df] * 1000;
                    });
                })
                $log.log("Found " + self.taskTypes.length + " task types")
            });
        }

        self.stopTask = function(task) {
            $log.log("Stopping task", task);
            $http({
                method: 'PUT',
                url: baseUrl + '/task/' + task.id + "/state?state=STOPPED" 
            }).then(function(d) {
                // Give it time to shut down before refreshing the list
                $log.log("Stopped", task);
                $timeout(self.refreshTasks, 1000);
            }, function(d) {
                $log.error("Problem stopping task", task);
            });
        }

        self.deleteTask = function(task) {
            $log.log("Deleting task", task);
            $http({
                method: 'DELETE',
                url: baseUrl + '/task/' + task.id
            }).then(function(d) {
                // Give it time to shut down before refreshing the list
                $log.log("Deleted", task);
                $timeout(self.refreshTasks, 1000);
            }, function(d) {
                $log.error("Problem deleting task", task);
            });
        }

        self.createTask = function(taskConf) {
            $log.log("Launching new task", taskConf);
            $http({
                method: 'POST',
                url: baseUrl + '/task/',
                data: {
                    "type": taskConf.type,
                    "environment": taskConf.environment
                }
            }).then(function(d) {
                // Give it time to start before refreshing the list
                $log.log("Launched", taskConf);
                $timeout(self.refreshTasks, 1000);
            }, function(d) {
                $log.error("Problem launching task", taskConf);
            });
        }
    }])
    .controller('TaskListCtl', ['$log', '$scope', '_', 'TasksStore', 'baseUrl', '_', function($log, $scope, _, TasksStore, baseUrl, _) {
        $scope.baseUrl = baseUrl;
        $scope.data = TasksStore;

        $scope.newTaskConf = (function() {
            var self = {};
            self.addingTask = false;
            self.newTaskType = undefined;

            self.changedTaskType = function() {
                self.newTask = {
                    environment: []
                };

                if (!self.newTaskType) {
                    return;
                }

                if (self.newTaskType.environment && self.newTaskType.environment.required) {
                    _.each(self.newTaskType.environment.required, function(v, k) {
                        self.newTask.environment.push({
                            name: v.name,
                            value: "",
                            description: v.description,
                            required: true
                        });
                    })
                }

                if (self.newTaskType.environment && self.newTaskType.environment.optional) {
                    _.each(self.newTaskType.environment.optional, function(v, k) {
                        self.newTask.environment.push({
                            name: v.name,
                            value: "",
                            description: v.description,
                            required: false
                        });
                    })
                }

                self.addParam();
            }

            self.launchTask = function() {
                // Transform object
                var cleanTask = {};
                cleanTask.type = self.newTaskType.name;
                cleanTask.environment = {};
                _.forEach(self.newTask.environment, function(v) {
                    cleanTask.environment[v.name] = v.value;
                })

                // Launch task
                TasksStore.createTask(cleanTask);

                // Reset form
                self.addingTask = false;
                self.newTaskType = undefined;
                self.changedTaskType();
            }

            self.addParam = function() {
                self.newTask.environment.push({
                    'key': '',
                    'value': ''
                })
            }

            self.removeParam = function(index) {
                self.newTask.environment.splice(index, 1);
            }

            // Initialize
            self.changedTaskType();

            return self;
        })();

        $scope.getStopCommand = function(task) {
            return task.isComplete ? "Delete" : "Stop";
        }
    }])
    .controller('TaskDetailCtl', ['$log', '$http', '$timeout', '$scope', '_', 'TasksStore', 'baseUrl', '_', '$stateParams', '$window',
        function($log, $http, $timeout, $scope, _, TasksStore, baseUrl, _, $stateParams, $window) {
        $scope.pinToBottom = false;

        $scope.baseUrl = baseUrl;
        $scope.events = [];
        $scope.taskId = $stateParams.taskId;
        $scope.jsonURL = baseUrl + '/task/' + $scope.taskId
        $scope.task = {};

        self.refreshTask = function() {
            return $http.get($scope.jsonURL).then(function(d) {
                $scope.task = d.data;
                TasksStore.cleanTask($scope.task);
            });
        }
        self.refreshTask();

        // Maybe: http://angular-ui.github.io/ui-router/site/#/api/ui.router.state.$uiViewScroll
        $scope.setScroll = function() {
            if ($scope.pinToBottom) {
                $log.log("Scrolling to botom", $scope.pinToBottom, document.body.scrollHeight)
                $timeout(function() {
                    $window.scrollTo(0, document.body.scrollHeight);
                }, 0);
            }
        };

        var source = new EventSource(baseUrl + '/task/' + $scope.taskId + '/log');
        $log.log("Starting to stream log events.")
        source.onmessage = function (event) {
            $scope.events.push(event);
            if ($scope.events.length > 100) {
                $scope.events.splice(0, 10);
            }
            $scope.$apply();
            $scope.setScroll();
        }
        source.onopen = function() {
            // Refresh task object
            self.refreshTask().then(function(){
                if ($scope.task.state != "RUNNING") {
                    // Don't keep reconnecting if no new content is coming in
                    source.close();
                    $log.log("Task is no longer running, closing even source.")
                }
            })
        }

        // How long has the task been running (in seconds)
        $scope.timeRunning = function(task) {
            if (!task.startedTs) {
                return undefined;
            }
            if (task.isComplete) {
                return (task.lastUpdatedTs - task.startedTs)/1000;
            }
            return ((new Date()).getTime() - task.startedTs)/1000;
        }

        $scope.$on("$destroy", function(){
            $log.log("Destroying scope; closing eventlistener for task log", $scope.taskId);
            source.close();
        })
    }])
    .controller('TaskTypeListCtl', ['$log', '$scope', '_', 'TasksStore', 'baseUrl', function($log, $scope, _, TasksStore, baseUrl) {
        $scope.baseUrl = baseUrl;
        $scope.data = TasksStore;
    }]);
