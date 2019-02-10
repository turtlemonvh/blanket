/*
 * Configuration
 */
var _ = require("lodash");
var gulp = require('gulp');
var notify = require("gulp-notify") ;
var concat = require("gulp-concat");
var sass = require('gulp-sass');
var size = require("gulp-size");
var uglify = require("gulp-uglify");
var jshint = require("gulp-jshint");
var debug = require('gulp-debug');
var templateCache = require('gulp-angular-templatecache');
var browserSync = require('browser-sync').create();

var reload = browserSync.reload;

/*
 * Constants
 */
const SRC_DIR_BASE = "./src"
const DEV_DIR_BASE = "./dev"
const PUB_DIR_BASE = "./public"

const JS_SRC_DIR = SRC_DIR_BASE + "/js";

const APP_DIST_DIR = "./public/js/app/";
const APP_DEV_DIR = "./dev/js/app/";

const EXTERNAL_LIBS = {
    jquery: "./node_modules/jquery/dist/jquery.js",
    angular: "./node_modules/angular/angular.js",
    bootstrap: "./node_modules/bootstrap/dist/js/bootstrap.js",
    lodash: "./bower_components/lodash/dist/lodash.js",
    uiRouter: "./bower_components/angular-ui-router/release/angular-ui-router.js",
    uiDateParser: "./node_modules/angular-ui-bootstrap/src/dateparser/dateparser.js"
};

const SIZE_OPTS = {
    showFiles: true,
    gzip: true
};
const LINT_OPTS = {
    unused: true,
    eqnull: true,
    jquery: true
};

function buildToSingleFile(options) {
    var src_glob = options.src_glob;  // array also allowed
    var tgt_filename = options.tgt_filename;
    var tgt_directory = options.tgt_directory;

    if (options.concatOnly) {
        return gulp.src(src_glob)
            // Log each file that will be concatenated into the common.js file.
            .pipe(debug({title: 'buildToSingleFile:foundFiles::'}))
            // Concatenate all files.
            .pipe(concat(tgt_filename))
            // Save that file to the appropriate location.
            .pipe(gulp.dest(tgt_directory));
    } else {
        return gulp.src(src_glob)
            // Log each file that will be concatenated into the common.js file.
            .pipe(size(SIZE_OPTS))
            // Concatenate all files.
            .pipe(concat(tgt_filename))
            // Minify the result.
            .pipe(uglify())
            // Log the new file size.
            .pipe(size(SIZE_OPTS))
            // Save that file to the appropriate location.
            .pipe(gulp.dest(tgt_directory));
    }
}

/**
 * Compress all angular templates into a single 'templates.js' file in the src directory
 */
var buildTemplateCache = function(){
    return gulp.src('src/templates/**/*.html')
        .pipe(templateCache('templates.js', {
            module: 'blanketApp'
        }))
        .pipe(gulp.dest('src/js'));
}
gulp.task("build-template-cache", buildTemplateCache);

/**
 * Linter for the most basic of quality assurance.
 */
gulp.task("lint", function() {
    return gulp.src(JS_SRC_DIR + "/**/*.js")
        .pipe(jshint(LINT_OPTS))
        .pipe(jshint.reporter("default"));
});

/*
 * Task to build the full contents of the app directory into a single file
 */
var buildApp = function(overrides){
    var overrides = overrides || {};

    return function(){
        var tgt_dir_base = overrides.tgt_dir_base || APP_DIST_DIR;
        var options = {
            // Always load module first
            src_glob: [JS_SRC_DIR + "/**/module.js", JS_SRC_DIR + "/**/*.js"],
            tgt_filename: "app.min.js",
            tgt_directory: tgt_dir_base 
        };
        _.each(overrides, function(v, k){
            options[k] = v;
        });
        buildToSingleFile(options);
    }
}
gulp.task("build-app", ["build-template-cache"], buildApp());
gulp.task("build-app-dev", ["build-template-cache"], buildApp({ concatOnly: true, tgt_dir_base: APP_DEV_DIR }));

/**
 * Externalize all site-wide libraries into one file.  Since these libraries are all sizable, it would be better for the
 * client to request it individually once and then retreive it from the cache than to include all of these files into
 * each and every browserified application. 
 */
var buildCommonLib = function(overrides){
    var overrides = overrides || {};

    return function(){
        var tgt_dir_base = overrides.tgt_dir_base || APP_DIST_DIR;
        var paths = [];

        // Get just the path to each externalizable lib.
        _.forEach(EXTERNAL_LIBS, function(path, name) {
            paths.push(path);
        });

        var options = {
            src_glob: paths,
            tgt_filename: "common.min.js",
            tgt_directory: tgt_dir_base + "../lib/"
        };
        _.each(overrides, function(v, k){
            options[k] = v;
        });
        buildToSingleFile(options);
    }
}
gulp.task("build-common-lib", buildCommonLib());
gulp.task("build-common-lib-dev", buildCommonLib({ concatOnly: true, tgt_dir_base: APP_DEV_DIR }));

var copyHtml = function(options){
    var options = options || {};
    var destBase = options.destBase || PUB_DIR_BASE;

    return function() {
        return gulp
            .src(SRC_DIR_BASE + '/index.html')
            .pipe(gulp.dest(destBase));
    }        
}
gulp.task("build-html", copyHtml());
gulp.task("build-html-dev", copyHtml({ destBase: DEV_DIR_BASE }));

var sassTask = function(options){
    // https://www.npmjs.com/package/gulp-sass
    var options = options || {};
    var dest = options.dest || PUB_DIR_BASE + "/css";
    return function() {
        return gulp.src(SRC_DIR_BASE + "/scss/**/*.scss")
            .pipe(sass({
                style: 'compressed',
                includePaths: [
                    "./node_modules/bootstrap-sass/assets/stylesheets"
                ]
            }).on("error", notify.onError(function (error) {
                return "Error: " + error.message;
            })))
            .pipe(gulp.dest(dest))
            .pipe(browserSync.stream());
    }
}
gulp.task('build-sass', sassTask({ dest: PUB_DIR_BASE + "/css" }));
gulp.task('build-sass-dev', sassTask({ dest: DEV_DIR_BASE + "/css" }));

gulp.task("build", ["build-common-lib", "build-app", "build-sass", "build-html"]);
gulp.task("build-dev", ["build-common-lib-dev", "build-app-dev", "build-sass-dev", "build-html-dev"]);


// Static Server + watching scss/html files
gulp.task('serve', ['build-dev'], function() {

    browserSync.init({
        server: DEV_DIR_BASE
    });

    gulp.watch(SRC_DIR_BASE + "/js/**/*.js", ['build-app-dev']);
    gulp.watch(SRC_DIR_BASE + "/scss/**/*.scss", ['build-sass-dev']);
    gulp.watch(SRC_DIR_BASE + "/*.html", ['build-html-dev']);
    gulp.watch(DEV_DIR_BASE + "/*.html").on('change', browserSync.reload);
});
gulp.task('default', ['serve']);
