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
        }

    }])
    .service('TasksStore', ['$http', 'baseUrl', '$log', '$timeout', 'LocalStore', function($http, baseUrl, $log, $timeout, localStorage) {
        var self = this;

        this.tasks = [];
        this.taskTypes = [];

        // FIXME: handle pagination and offsets
        self.refreshTasks = function() {
            $http.get(baseUrl + '/task/?limit=50&reverseSort=true').then(function(d) {
                self.tasks = d.data;
                _.each(self.tasks, function(v) {
                    var labelClasses = {
                        "WAIT": "default",
                        "START": "primary",
                        "RUNNING": "warning",
                        "ERROR": "danger",
                        "SUCCESS": "success"
                    };
                    v.labelClass = labelClasses[v.state];
                    v.hasResults = _.intersection(["ERROR", "SUCCESS"], [v.state]).length !== 0;

                    // Date fixing
                    var dateFields = ['createdTs', 'startedTs', 'lastUpdatedTs'];
                    _.each(dateFields, function(df) {
                        v[df] = v[df] * 1000;
                    });
                })
                $log.log("Found " + self.tasks.length + " tasks")
            });
        }

        self.refreshTaskTypes = function() {
            $http.get(baseUrl + '/task_type/?limit=50').then(function(d) {
                self.taskTypes = d.data;
                _.each(self.taskTypes, function(v) {
                    // Date fixing
                    var dateFields = ['_loaded_ts'];
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
    }])
    .controller('TaskListCtl', ['$log', '$http', '$interval', '$scope', '_', 'TasksStore', 'baseUrl', '_', function($log, $http, $interval, $scope, _, TasksStore, baseUrl, _) {
        $scope.baseUrl = baseUrl;
        $scope.data = TasksStore;

        $scope.newTaskConf = (function() {
            var self = {};
            self.addingTask = false;

            self.clearTask = function() {
                self.newTask = {
                    environment: []
                };
                self.addParam();
                self.newTaskType = undefined;
            }

            self.launchTask = function() {
                // Transform object
                var cleanTask = {};
                cleanTask.type = self.newTaskType.name;
                cleanTask.environment = {};
                _.forEach(self.newTask.environment, function(v) {
                    cleanTask.environment[v.key] = v.value;
                })

                // Launch task
                TasksStore.createTask(cleanTask);

                // Reset form
                self.addingTask = false;
                self.clearTask();
            }

            self.removeParam = function(index) {
                self.newTask.environment.splice(index, 1);
            }

            self.addParam = function() {
                self.newTask.environment.push({
                    'key': '',
                    'value': ''
                });
            }

            // Initialize
            self.clearTask();

            return self;
        })();

        $scope.getStopCommand = function(task) {
            return task.hasResults ? "Delete" : "Stop";
        }
    }])
    .controller('TaskTypeListCtl', ['$log', '$http', '$interval', '$scope', '_', 'TasksStore', 'baseUrl', function($log, $http, $interval, $scope, _, TasksStore, baseUrl) {
        $scope.baseUrl = baseUrl;
        $scope.data = TasksStore;
    }]);
