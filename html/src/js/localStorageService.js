angular.module('blanketApp')
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
