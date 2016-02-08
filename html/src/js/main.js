angular.module('blanketApp')
    .service('TasksStore', ['$http', 'baseUrl', '$log', '$interval', 'LocalStore', function($http, baseUrl, $log, $interval, localStorage) {
        var self = this;

        this.tasks = [];
        this.taskTypes = [];
        var shouldRefresh = localStorage.getItem("blanket.shouldRefresh") == 'true';

        this.getRefreshValue = function() { return shouldRefresh; }
        this.setAutoRefresh = function(v) {
            shouldRefresh = v;
            localStorage.setItem("blanket.shouldRefresh", v);
            var status = shouldRefresh ? "on" : "off";
            $log.log('Turning ' + status + ' autorefresh')
        }

        // FIXME: handle pagination and offsets
        var refreshData = function() {
            $http.get(baseUrl + '/task/?limit=10').then(function(d) {
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

            $http.get(baseUrl + '/task_type/?limit=10').then(function(d) {
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
