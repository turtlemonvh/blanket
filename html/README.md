# Blanket UI

## Quick start

Running locally with reload on change:

    # Install
    npm install
    bower install

    # Run dev server; build content too
    gulp serve

Building compressed/minified version

    gulp build

Checking syntax of js files

    gulp lint

## Tech stack

* single page angular application served from index.html
* bootstrap scss
    * http://getbootstrap.com/css/#sass
    * https://github.com/twbs/bootstrap-sass#d-npm--nodejs
    * https://gist.github.com/ericbarnes/ac3ae075c97c1073869c
* gulp / live reload
    * https://browsersync.io/
    * https://browsersync.io/docs/gulp/
* angular js
    * https://angularjs.org/


## TODO

* limit results to first 100 items
* allow editing of TOML files inline
    * edit button that pops up form
* integrate stuff from html5bp
    * https://github.com/h5bp/html5-boilerplate/tree/5.3.0
* move this to its own repo
* integrate option to serve this into main blanket app
* add some command line flags to make top label commands easier to work with
    * http://stackoverflow.com/questions/23023650/is-it-possible-to-pass-a-flag-to-gulp-to-have-it-run-tasks-in-different-ways
* move some code to external libraries to make it easier to include in other projects
