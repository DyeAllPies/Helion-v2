// eslint.config.js — ESLint 9 flat config for angular-eslint 21
// Uses @angular-eslint packages directly (no typescript-eslint wrapper needed)

const angularEslint        = require("@angular-eslint/eslint-plugin");
const angularEslintTemplate = require("@angular-eslint/eslint-plugin-template");
const templateParser       = require("@angular-eslint/template-parser");
const tsParser             = require("@typescript-eslint/parser");
const tsPlugin             = require("@typescript-eslint/eslint-plugin");

module.exports = [
  // ── TypeScript source files ─────────────────────────────────────────────────
  {
    files: ["src/**/*.ts"],
    languageOptions: {
      parser: tsParser,
      parserOptions: {
        project: ["tsconfig.eslint.json"],
        tsconfigRootDir: __dirname,
      },
    },
    plugins: {
      "@typescript-eslint":  tsPlugin,
      "@angular-eslint":     angularEslint,
    },
    rules: {
      ...tsPlugin.configs["recommended"].rules,
      "@angular-eslint/component-selector": [
        "error", { type: "element", prefix: "app", style: "kebab-case" }
      ],
      "@angular-eslint/directive-selector": [
        "error", { type: "attribute", prefix: "app", style: "camelCase" }
      ],
      "@typescript-eslint/no-explicit-any":  "warn",
      "@typescript-eslint/no-unused-vars":   ["error", { argsIgnorePattern: "^_" }],
      "no-console":                          ["warn", { allow: ["error", "warn"] }],
    },
  },

  // ── HTML templates ──────────────────────────────────────────────────────────
  {
    files: ["src/**/*.html"],
    languageOptions: {
      parser: templateParser,
    },
    plugins: {
      "@angular-eslint/template": angularEslintTemplate,
    },
    rules: {
      "@angular-eslint/template/banana-in-box":   "error",
      "@angular-eslint/template/no-negated-async": "warn",
    },
  },
];
