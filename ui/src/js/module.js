angular.module('blanketApp', ["ui.router", "ui.bootstrap.dateparser"])
    .config(["$stateProvider", "$urlRouterProvider", function($stateProvider, $urlRouterProvider) {
        // For any unmatched url, redirect to home
        $urlRouterProvider.otherwise("/");

        // Set up the states
        $stateProvider
        .state('home', {
            url: "/",
            templateProvider: function($templateCache){
                // FIXME: Add dashboard
                //return $templateCache.get('home.html');
                return $templateCache.get('tasks.html');
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
        .state('task-detail', {
            url: "/task/:taskId",
            templateProvider: function($templateCache){
                return $templateCache.get('task-detail.html');
            }
        })
        .state('about', {
            url: "/about",
            templateProvider: function($templateCache){
                return $templateCache.get('about.html');
            }
        });
    }])
    // Mute unhandled exceptions. Really we want to handle these, but we want to upgrade to Angular 6 anyway...
    // https://www.codelord.net/2017/08/20/angular-1-dot-6-s-possibly-unhandled-rejection-errors/
    .config(function ($qProvider) {
        $qProvider.errorOnUnhandledRejections(false);
    });