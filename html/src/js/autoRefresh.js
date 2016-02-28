angular.module('blanketApp')
    .service('AutorefreshService', ['$http', '$q', 'baseUrl', '$log', '$interval', 'LocalStore', 'TasksStore', 'WorkerStore', 
        function($http, $q, baseUrl, $log, $interval, localStorage, TasksStore, WorkerStore) {

        var self = this;
        var shouldRefresh = localStorage.getItem("blanket.shouldRefresh") == 'true';
        var ready = $q.defer();
        this.ready = ready.promise;

        this.getRefreshValue = function() { return shouldRefresh; }
        this.setAutoRefresh = function(v) {
            shouldRefresh = v;
            localStorage.setItem("blanket.shouldRefresh", v);
            var status = shouldRefresh ? "on" : "off";
            $log.log('Turning ' + status + ' autorefresh')
        }

        self.refreshData = function() {
            var tasksReady = TasksStore.refreshTasks();
            var taskTypesReady = TasksStore.refreshTaskTypes();
            var workersReady = WorkerStore.refreshList();
            return $q.all([tasksReady, taskTypesReady, workersReady])
        }

        // Call it and keep calling it
        self.refreshData().then(function(){
            ready.resolve();
        });
        $interval(function(){
            if (shouldRefresh) {
                self.refreshData();
            } else {
                $log.log('Skipping autorefresh')
            }
        }, 2000);
    }]);
