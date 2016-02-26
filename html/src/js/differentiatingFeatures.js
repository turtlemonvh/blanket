angular.module('blanketApp')
    .service('diffFeatures', ['$log', '$interval', '_', function($log, $interval, _) {
        // Takes a list of items and a function to use to get the feature from each item
        var self = this;

        // Extractor is a function that extracts string features from an object
        self.df = function(extractor) {
            this.counts = {};
            this.extractor = extractor;
        }

        // Item is an object
        self.df.prototype.addItem = function(item) {
            var df = this;
            var features = df.extractor(item);

            // Keep track of the # occurances of each feature
            _.each(features, function(ft) {
                df.counts[ft] = (df.counts[ft] || 0) + 1;
            })

            // Return list of features
            return features;
        }

        self.df.prototype.getBestOfFeatures = function(features, nfeatures) {
            var df = this;
            var nfeatures = nfeatures || 3;

            // Sorts lowest to highest and returns nfeatures items
            return _.take(_.sortBy(features, function(ft) {
                return df.counts[ft];
            }), nfeatures);
        }


    }])
