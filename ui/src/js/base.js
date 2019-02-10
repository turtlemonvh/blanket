angular.module('blanketApp')
    .constant('_', window._ )
    .constant('baseUrl', 'http://localhost:8773')
    .controller('NavCtl', ['$scope', '$interval', 'AutorefreshService', function($scope, $interval, AutorefreshService) {
        $scope.autoRefresh = AutorefreshService.getRefreshValue();
        $scope.toggleAutoRefresh = function() { AutorefreshService.setAutoRefresh($scope.autoRefresh); }

        $scope.lastRefreshed = Date.now();
        $interval(function(){
            $scope.lastRefreshed = Date.now();
        }, 200);
    }]);
