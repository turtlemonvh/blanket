angular.module('blanketApp', ["ui.router"])
    .config(["$stateProvider", "$urlRouterProvider", function($stateProvider, $urlRouterProvider) {
        // For any unmatched url, redirect to home
        $urlRouterProvider.otherwise("/");

        // Set up the states
        $stateProvider
        .state('home', {
            url: "/",
            templateProvider: function($templateCache){
                return $templateCache.get('home.html');
            }
        })
        .state('tasks', {
            url: "/tasks",
            templateProvider: function($templateCache){
                return $templateCache.get('tasks.html');
            }
        })
        .state('taskTypes', {
            url: "/task-types",
            templateProvider: function($templateCache){
                return $templateCache.get('task-types.html');
            }
        })
        .state('workers', {
            url: "/workers",
            templateProvider: function($templateCache){
                return $templateCache.get('workers.html');
            }
        })
        .state('about', {
            url: "/about",
            templateProvider: function($templateCache){
                return $templateCache.get('about.html');
            }
        });
    }]);