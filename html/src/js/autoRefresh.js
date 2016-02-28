angular.module('blanketApp')
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
    }]);
