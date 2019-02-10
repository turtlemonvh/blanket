angular.module('blanketApp')
    .controller('AboutCtrl', ['$http', '$log', '$scope', 'baseUrl', function($http, $log, $scope, baseUrl) {
        $scope.baseUrl = baseUrl;
        $scope.appConfig = {};

        $scope.refreshList = function() {
            $http.get(baseUrl + '/config/').then(function(d) {
                $scope.appConfig = d.data;
                $log.log("Fetched config")
            });
        }

        $scope.refreshList();
    }]);
