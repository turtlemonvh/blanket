angular.module('blanketApp', [])
    .service('')
    .constant('_', window._ )
    .constant('baseUrl', 'http://localhost:8773')
    .controller('NavCtl', ['$scope', '$interval', function($scope, $interval){
        $scope.lastRefreshed = Date.now();
        $interval(function(){
            $scope.lastRefreshed = Date.now();
        }, 200);
    }])
    .controller('TaskListCtl', ['$log', '$http', '$scope', 'baseUrl', '_', function($log, $http, $scope, baseUrl, _) {
        var taskList = this;
        $scope.tasks = [];
        $scope.taskTypes = [];
        $scope.baseUrl = baseUrl;

        $http.get(baseUrl + '/task/').then(function(d) {
            $scope.tasks = d.data;
            _.each($scope.tasks, function(v) {
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
        });


        $http.get(baseUrl + '/task_type/').then(function(d) {
            $scope.taskTypes = d.data;
            _.each($scope.taskTypes, function(v) {
                // Date fixing
                var dateFields = ['_loaded_ts'];
                _.each(dateFields, function(df) {
                    v[df] = v[df] * 1000;
                });
            })
        });
    }]);
