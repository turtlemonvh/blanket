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

        self.deleteWorker = function(worker) {
            $log.log("Deleting worker", worker);
            $http({
                method: 'DELETE',
                url: baseUrl + '/worker/' + worker.id
            }).then(function(d) {
                // Give it time to shut down before refreshing the list
                $log.log("Deleting worker", worker);
                $timeout(self.refreshList, 500);
            }, function(d) {
                $log.error("Problem deleting worker", worker);
            });
        }

        self.stopWorker = function(worker) {
            $log.log("Stopping worker", worker);
            $http({
                method: 'PUT',
                url: baseUrl + '/worker/' + worker.id + '/stop'
            }).then(function(d) {
                // Give it time to shut down before refreshing the list
                $log.log("Shut down worker", worker);
                $timeout(self.refreshList, 500);
            }, function(d) {
                $log.error("Problem shutting down worker", worker);
            });
        }

        self.restartWorker = function(worker) {
            $log.log("Restarting worker", worker);
            $http({
                method: 'PUT',
                url: baseUrl + '/worker/' + worker.id + '/restart'
            }).then(function(d) {
                // Give it time to shut down before refreshing the list
                $log.log("Restarted worker", worker);
                $timeout(self.refreshList, 500);
            }, function(d) {
                $log.error("Problem restarting worker", worker);
            });
        }


        self.launchWorker = function(workerConf) {
            $log.log("Creating new worker", workerConf);
            workerConf.tags = workerConf.rawTags.split(",");
            $http({
                method: 'POST',
                url: baseUrl + '/worker/',
                data: workerConf
            }).then(function(d) {
                // Give it time to start up before refreshing the list
                $log.log("Launched worker", workerConf);
                $timeout(self.refreshList, 500);
            }, function(d) {
                $log.error("Problem launcing worker", workerConf);
            });
        }

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

    }]);