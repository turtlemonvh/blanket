# Blanket UI

A browser UI for [blanket-api](https://github.com/turtlemonvh/blanket-api).

## Quick start

Running locally with reload on change:

    # Install
    npm install
    npm install -g bower gulp
    npm install --save-dev jshint gulp-jshint
    bower install

    # Run dev server; build content too
    gulp serve

Building compressed/minified version

    gulp build

Checking syntax of js files

    gulp lint

## Integrating with main blanket project

    # In html dir of this project
    gulp build-app

    # In blanket dir; re-grab binary data and rebuild
    make update-bindata
    make <platform>

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
