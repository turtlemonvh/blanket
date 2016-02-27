angular.module('blanketApp')
    .service('WorkerStore', ['$http', 'baseUrl', '$log', '$timeout', function($http, baseUrl, $log, $timeout) {
        var self = this;
        self.workers = [];

        self.refreshList = function() {
            $http.get(baseUrl + '/worker/').then(function(d) {
                self.workers = d.data;
                _.each(self.workers, function(v) {
                    // Date fixing
                    var dateFields = ['startedTs'];
                    _.each(dateFields, function(df) {
                        v[df] = v[df] * 1000;
                    });
                })
                $log.log("Found " + self.workers.length + " workers")
            });
        }

        self.stopWorker = function(worker) {
            $log.log("Stopping worker", worker);
            $http({
                method: 'PUT',
                url: baseUrl + '/worker/' + worker.pid + '/shutdown'
            }).then(function(d) {
                // Give it time to shut down before refreshing the list
                $log.log("Shut down worker", worker);
                $timeout(self.refreshList, worker.checkInterval*1000 + 500);
            }, function(d) {
                $log.error("Problem shutting down worker", worker);
            });
        }

        self.launchWorker = function(workerConf) {
            $log.log("Creating new worker", workerConf);
            $http({
                method: 'POST',
                url: baseUrl + '/worker/',
                data: workerConf
            }).then(function(d) {
                // Give it time to start up before refreshing the list
                $log.log("Launched worker", workerConf);
                $timeout(self.refreshList, workerConf.checkInterval*1000 + 1000);
            }, function(d) {
                $log.error("Problem launcing worker", workerConf);
            });
        }

    }])
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
                "SUCCESS": "success"
            };
            t.labelClass = labelClasses[t.state];
            t.hasResults = _.intersection(["ERROR", "SUCCESS", "START", "RUNNING"], [t.state]).length !== 0;

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
    .service('AutorefreshService', ['$http', 'baseUrl', '$log', '$interval', 'LocalStore', 'TasksStore', 'WorkerStore', 
        function($http, baseUrl, $log, $interval, localStorage, TasksStore, WorkerStore) {

        var self = this;
        var shouldRefresh = localStorage.getItem("blanket.shouldRefresh") == 'true';

        this.getRefreshValue = function() { return shouldRefresh; }
        this.setAutoRefresh = function(v) {
            shouldRefresh = v;
            localStorage.setItem("blanket.shouldRefresh", v);
            var status = shouldRefresh ? "on" : "off";
            $log.log('Turning ' + status + ' autorefresh')
        }

        self.refreshData = function() {
            TasksStore.refreshTasks();
            TasksStore.refreshTaskTypes();
            WorkerStore.refreshList();
        }

        // Call it and keep calling it
        self.refreshData();
        $interval(function(){
            if (shouldRefresh) {
                self.refreshData();
            } else {
                $log.log('Skipping autorefresh')
            }
        }, 2000);
    }])
    .constant('_', window._ )
    .constant('baseUrl', 'http://localhost:8773')
    .controller('NavCtl', ['$scope', '$interval', 'AutorefreshService', function($scope, $interval, AutorefreshService) {
        $scope.autoRefresh = AutorefreshService.getRefreshValue();
        $scope.toggleAutoRefresh = function() { AutorefreshService.setAutoRefresh($scope.autoRefresh); }

        $scope.lastRefreshed = Date.now();
        $interval(function(){
            $scope.lastRefreshed = Date.now();
        }, 200);
    }])
    .controller('WorkerListCtrl', ['$log', '$scope', 'WorkerStore', 'baseUrl', function($log, $scope, WorkerStore, baseUrl) {
        $scope.baseUrl = baseUrl;
        $scope.data = WorkerStore;

        $scope.newWorkerConf = (function() {
            var self = {};
            self.addingWorker = false;

            self.clearForm = function() {
                self.newWorker = {
                    checkInterval: 2
                };
            }

            self.launchWorker = function() {
                $log.log("Launching worker", self.newWorker)

                // Transform object
                self.newWorker.checkInterval = +self.newWorker.checkInterval;

                // Launch task
                WorkerStore.launchWorker(self.newWorker);

                // Reset form
                self.addingWorker = false;
                self.clearForm();
            }

            // Initialize
            self.clearForm();

            return self;
        })();

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

                _.each(self.newTaskType.environment.required, function(v, k) {
                    self.newTask.environment.push({
                        name: v.name,
                        value: "",
                        description: v.description,
                        required: true
                    });
                })

                _.each(self.newTaskType.environment.optional, function(v, k) {
                    self.newTask.environment.push({
                        name: v.name,
                        value: "",
                        description: v.description,
                        required: false
                    });
                })

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
            return task.hasResults ? "Delete" : "Stop";
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

        $scope.$on("$destroy", function(){
            $log.log("Destroying scope; closing eventlistener for task log", $scope.taskId);
            source.close();
        })
    }])
    .controller('TaskTypeListCtl', ['$log', '$scope', '_', 'TasksStore', 'baseUrl', function($log, $scope, _, TasksStore, baseUrl) {
        $scope.baseUrl = baseUrl;
        $scope.data = TasksStore;
    }]);
