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
    jquery: "./node_modules/jquery/dist/jquery.min.js",
    angular: "./node_modules/angular/angular.min.js",
    bootstrap: "./node_modules/bootstrap/dist/js/bootstrap.min.js"
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
 * Linter for the most basic of quality assurance.
 */
gulp.task("lint", function() {
    return gulp.src(JS_SRC_DIR + "**/*.js")
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
            src_glob: JS_SRC_DIR + "/**/*.js",
            tgt_filename: "app.min.js",
            tgt_directory: tgt_dir_base 
        };
        _.each(overrides, function(v, k){
            options[k] = v;
        });
        buildToSingleFile(options);
    }
}
gulp.task("build-app", buildApp())
gulp.task("build-app-dev", buildApp({ concatOnly: true, tgt_dir_base: APP_DEV_DIR }))

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
gulp.task('sass', sassTask({ dest: PUB_DIR_BASE + "/css" }));
gulp.task('sass-dev', sassTask({ dest: DEV_DIR_BASE + "/css" }));

gulp.task("build", ["build-common-lib", "build-app", "sass", "build-html"]);
gulp.task("build-dev", ["build-common-lib-dev", "build-app-dev", "sass-dev", "build-html-dev"]);


// Static Server + watching scss/html files
gulp.task('serve', function() {

    browserSync.init({
        server: DEV_DIR_BASE
    });

    gulp.watch(SRC_DIR_BASE + "/js/**/*.js", ['build-app-dev']);
    gulp.watch(SRC_DIR_BASE + "/scss/**/*.scss", ['sass-dev']);
    gulp.watch(SRC_DIR_BASE + "/*.html", ['build-html-dev']);
    gulp.watch(DEV_DIR_BASE + "/*.html").on('change', browserSync.reload);
});
gulp.task('default', ['serve']);
