// dashboard/karma.conf.js
module.exports = function (config) {
  config.set({
    basePath: '',
    frameworks: ['jasmine', '@angular-devkit/build-angular'],
    plugins: [
      require('karma-jasmine'),
      require('karma-chrome-launcher'),
      require('karma-jasmine-html-reporter'),
      require('karma-coverage'),
      require('@angular-devkit/build-angular/plugins/karma'),
    ],
    client: {
      jasmine: { random: true },
      clearContext: false,
    },
    jasmineHtmlReporter: { suppressAll: true },
    coverageReporter: {
      dir: require('path').join(__dirname, './coverage/helion-dashboard'),
      subdir: '.',
      // json-summary produces coverage-summary.json, which
      // scripts/check-dashboard-coverage.sh reads to enforce thresholds.
      //
      // The built-in `check:` block below is IGNORED by
      // @angular-devkit/build-angular:karma — the Angular test builder
      // overrides the coverage config internally (the thresholds are
      // reported via the text-summary reporter but never enforced as a
      // non-zero exit). External enforcement via the Makefile is the
      // only reliable path today.
      reporters: [
        { type: 'html' },
        { type: 'text-summary' },
        { type: 'lcovonly' },
        { type: 'json-summary' },
      ],
      check: {
        global: {
          statements: 85,
          branches:   60,
          functions:  85,
          lines:      85,
        },
      },
    },
    reporters: ['progress', 'kjhtml'],
    port: 9876,
    colors: true,
    logLevel: config.LOG_INFO,
    autoWatch: true,
    browsers: ['Chrome'],
    singleRun: false,
    restartOnFileChange: true,
  });
};
