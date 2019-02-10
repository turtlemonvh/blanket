angular.module('blanketApp')
    .service('TasksStore', ['$http', 'baseUrl', '$log', '$timeout', 'diffFeatures', '_', 'LocalStore', 'uibDateParser',
    function($http, baseUrl, $log, $timeout, diffFeatures, _, localStorage, dateParser) {
        var self = this;
        this.queryMaxItems = 50;

        var baseDate = new Date();
        baseDate.setSeconds(0)

        this.tasks = [];
        this.taskTypes = [];

        this.taskLabelClasses = {
            "WAITING": "default",
            "CLAIMED": "primary",
            "RUNNING": "warning",
            "ERROR": "danger",
            "SUCCESS": "success",
            "TIMEDOUT": "danger",
            "STOPPED": "danger"
        };

        self.parseDate = function(dateString) {
            return dateParser.parse(dateString, 'yyyy/M!/d! H:mm', baseDate);
        }

        self.cleanTask = function(t) {
            t.labelClass = self.taskLabelClasses[t.state];
            t.hasResults = _.intersection(["WAITING", "CLAIMED"], [t.state]).length === 0;
            t.isComplete = _.intersection(["WAITING", "CLAIMED", "RUNNING"], [t.state]).length === 0;

            // Date fixing
            var dateFields = ['createdTs', 'startedTs', 'lastUpdatedTs'];
            _.each(dateFields, function(df) {
                t[df] = t[df] * 1000;
            });
        }

        self.cleanTaskType = function(tt) {
            // Date fixing
            var dateFields = ['loadedTs'];
            _.each(dateFields, function(df) {
                tt[df] = tt[df] * 1000;
            });
        }

        self.taskFilterConfig = {};

        self.loadTaskFilterConfig = function() {
            var conf = localStorage.getItem("blanket.taskFilters") || "{}";
            $log.log("Loading task filters", conf);
            var o = JSON.parse(conf);
            _.each(o, function(v, k) {
                self.taskFilterConfig[k] = v;
            });
        }
        self.loadTaskFilterConfig();

        function setTaskFilterConfig(fc) {
            self.taskFilterConfig = {
                tags: fc.tags, // comma separated
                taskTypes: fc.taskTypes,
                states: fc.states,
                startDate: fc.startDate,
                endDate: fc.endDate,
            }
            localStorage.setItem("blanket.taskFilters", JSON.stringify(self.taskFilterConfig));
            self.refreshTasks();
        }
        self.setTaskFilterConfig = _.debounce(setTaskFilterConfig, 500);


        // FIXME: handle pagination and offsets
        self.refreshTasks = function() {
            var queryString = '/task/?reverseSort=true';
            queryString += "&limit=" + self.queryMaxItems;

            if (self.taskFilterConfig.tags && self.taskFilterConfig.tags !== "") {
                queryString += "&requiredTags=" + self.taskFilterConfig.tags;
            }
            if (self.taskFilterConfig.taskTypes && self.taskFilterConfig.taskTypes.length) {
                queryString += "&types=" + _.join(self.taskFilterConfig.taskTypes, ",");
            }
            if (self.taskFilterConfig.states && self.taskFilterConfig.states.length) {
                queryString += "&states=" + _.join(self.taskFilterConfig.states, ",");
            }
            var parsedStartDate = self.parseDate(self.taskFilterConfig.startDate);
            var parsedEndDate = self.parseDate(self.taskFilterConfig.endDate);
            if (parsedStartDate) {
                queryString += "&createdAfter=" + Math.floor(parsedStartDate.getTime() / 1000);
            }
            if (parsedEndDate) {
                queryString += "&createdBefore=" + Math.floor(parsedEndDate.getTime() / 1000);
            }

            var r = $http.get(baseUrl + queryString).then(function(d) {
                self.tasks = d.data;
                _.each(self.tasks, function(v) {
                    self.cleanTask(v);
                })
                $log.log("Found " + self.tasks.length + " tasks")
            });

            return r.then(function() {

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
            var queryString = '/task_type/?limit=' + self.queryMaxItems;
            return $http.get(baseUrl + queryString).then(function(d) {
                self.taskTypes = d.data;
                _.each(self.taskTypes, function(v) {
                    self.cleanTaskType(v);
                })
                $log.log("Found " + self.taskTypes.length + " task types")
            });
        }

        self.stopTask = function(task) {
            $log.log("Canceling task", task);
            return $http({
                method: 'PUT',
                url: baseUrl + '/task/' + task.id + "/cancel" 
            }).then(function(d) {
                // Give it time to shut down before refreshing the list
                $log.log("Canceled", task);
                $timeout(self.refreshTasks, 1000);
            }, function(d) {
                $log.error("Problem canceling task", task);
            });
        }

        self.deleteTask = function(task) {
            $log.log("Deleting task", task);
            return $http({
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
            return $http({
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
    .controller('TaskListCtl', ['$log', '$scope', '_', 'TasksStore', 'AutorefreshService', 'baseUrl', '_', 'uibDateParser',
    function($log, $scope, _, TasksStore, AutorefreshService, baseUrl, _, dateParser) {
        $scope.baseUrl = baseUrl;
        $scope.data = TasksStore;
        $scope.taskStates = _.keys(TasksStore.taskLabelClasses);
        $scope.taskTypeNames = [];

        var currentFilterDescription = "";
        var fc = {
            loaded: false,
            editing: false,
            tags: "",
            taskTypes: [],
            states: [],
            startDate: "",
            endDate: "",
            startDateParsed: undefined,
            endDateParsed: undefined,
            getDescriptionPrefix: function() {
                if (TasksStore.tasks.length == TasksStore.queryMaxItems) {
                    return "Showing first " + TasksStore.queryMaxItems + " tasks";
                } else {
                    return "Showing all tasks";
                }
            },
            getDescriptionPostfix: function() {
                var s = "";

                fc.startDateParsed = TasksStore.parseDate(fc.startDate);
                fc.endDateParsed = TasksStore.parseDate(fc.endDate);
                if (fc.startDateParsed && fc.endDateParsed) {
                    s += " created between " + fc.startDateParsed + " and " + fc.endDateParsed;
                } else if (fc.startDateParsed) {
                    s += " created after " + fc.startDateParsed;
                } else if (fc.endDateParsed) {
                    s += " created before " + fc.endDateParsed;
                }

                if (fc.states.length !== 0) {
                    s += " in states [" + _.join(fc.states, ",") + "]"
                }

                if (fc.tags !== "") {
                    s += " with tags in [" + fc.tags + "]"
                }

                if (fc.taskTypes.length !== 0) {
                    s += " of types [" + _.join(fc.taskTypes, ",") + "]";
                }

                if (s !== currentFilterDescription && fc.loaded) {
                    TasksStore.setTaskFilterConfig(fc);
                }

                currentFilterDescription = s;
                return s;
            }
        };
        $scope.filterConfig = fc;

        // As soon as data is ready set the form
        AutorefreshService.ready.then(function(){
            // Set the task type
            if (!$scope.newTaskConf.newTaskType && $scope.data.taskTypes.length !== 0) {
                $scope.newTaskConf.newTaskType = $scope.data.taskTypes[0];
            }
            $scope.newTaskConf.changedTaskType();
            $scope.taskTypeNames = _.map($scope.data.taskTypes, 'name');

            // Load filter config from localStorage
            if (fc.loaded) return;
            _.each(TasksStore.taskFilterConfig, function(v, k) {
                fc[k] = v;
            });
            fc.loaded = true;
        });

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
    }])
    .controller('TaskDetailCtl', ['$log', '$http', '$timeout', '$scope', '_', 'TasksStore', 'baseUrl', '_', '$stateParams', '$window',
        function($log, $http, $timeout, $scope, _, TasksStore, baseUrl, _, $stateParams, $window) {
        $scope.pinToBottom = false;

        $scope.baseUrl = baseUrl;
        $scope.events = [];
        $scope.taskId = $stateParams.taskId;
        $scope.jsonURL = baseUrl + '/task/' + $scope.taskId
        $scope.task = {};
        $scope.taskType = {};

        self.refreshTask = function() {
            return $http.get($scope.jsonURL).then(function(d) {
                $scope.task = d.data;
                TasksStore.cleanTask($scope.task);
            });
        }
        self.refreshTaskType = function() {
            return $http.get(baseUrl + "/task_type/" + $scope.task.type).then(function(d) {
                $scope.taskType = d.data;
                TasksStore.cleanTaskType($scope.taskType);
            });
        }
        self.refreshTask().then(self.refreshTaskType);

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
