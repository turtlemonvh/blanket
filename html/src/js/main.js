angular.module('blanketApp', [])
    .service('LocalStore', ['$log', '$interval', function($log, $interval) {
        // Localstorage
        var self = this;
        self.ready = false;
        self.canStore = false;
        var notReady = function(){
            $log.log("Too fast! Storage is not available yet!");
        }
        var noop = function(){
            $log.log("localStorage api not available on this machine.");
        }
        self.setItem = notReady;
        self.getItem = notReady;
        self.removeItem = notReady;
        self.clear = notReady;

        // https://developer.mozilla.org/en-US/docs/Web/API/Web_Storage_API/Using_the_Web_Storage_API
        function storageAvailable(type) {
            try {
                var storage = window[type],
                    x = '__storage_test__';
                storage.setItem(x, x);
                storage.removeItem(x);
                return true;
            }
            catch(e) {
                return false;
            }
        }
        self.canStore = storageAvailable('localStorage');
        self.ready = true;

        if (self.canStore) {
            self.setItem = function(k, v) {
                return localStorage.setItem(k, v);
            }
            self.getItem = function(k) {
                return localStorage.getItem(k);
            }
            self.removeItem = function(k) {
                return localStorage.removeItem(k);
            }
            self.clear = function() {
                return localStorage.clear();
            }
        } else {
            self.setItem = noop;
            self.getItem = noop;
            self.removeItem = noop;
            self.clear = noop;
        }
    }])
    .service('TasksStore', ['$http', 'baseUrl', '$log', '$interval', 'LocalStore', function($http, baseUrl, $log, $interval, localStorage) {
        var self = this;

        this.tasks = [];
        this.taskTypes = [];
        var shouldRefresh = localStorage.getItem("blanket.shouldRefresh") == 'true';

        this.getRefreshValue = function() { return shouldRefresh; }
        this.setAutoRefresh = function(v) {
            shouldRefresh = v;
            var status = shouldRefresh ? "on" : "off";
            $log.log('Turning ' + status + ' autorefresh')
        }

        var refreshData = function() {
            $http.get(baseUrl + '/task/').then(function(d) {
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

            $http.get(baseUrl + '/task_type/').then(function(d) {
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

        // Call it and keep calling it
        refreshData();
        $interval(function(){
            if (shouldRefresh) {
                refreshData();
            } else {
                $log.log('Skipping autorefresh')
            }
        }, 2000);
    }])
    .constant('_', window._ )
    .constant('baseUrl', 'http://localhost:8773')
    .controller('NavCtl', ['$scope', '$interval', 'TasksStore', function($scope, $interval, TasksStore) {
        $scope.autoRefresh = TasksStore.getRefreshValue();
        $scope.toggleAutoRefresh = function() { TasksStore.setAutoRefresh($scope.autoRefresh); }

        $scope.lastRefreshed = Date.now();
        $interval(function(){
            $scope.lastRefreshed = Date.now();
        }, 200);
    }])
    .controller('TaskListCtl', ['$log', '$http', '$interval', '$scope', '_', 'TasksStore', 'baseUrl', function($log, $http, $interval, $scope, _, TasksStore, baseUrl) {
        $scope.baseUrl = baseUrl;
        $scope.data = TasksStore;
    }]);
