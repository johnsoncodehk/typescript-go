// @module: commonjs
// @declaration: true
// @allowJs: true
// @checkJs: true
// @outDir: ./out

// @filename: /node_modules/@types/dep/index.d.ts
declare namespace dep {
    interface Options {
        expand?: (str: string) => string;
    }
}
declare function dep(pattern: string, options?: dep.Options): string[];
export = dep;

// @filename: /node_modules/@types/dep/package.json
{ "name": "@types/dep", "version": "1.0.0" }

// @filename: /node_modules/@types/extpkg/index.d.ts
import dep = require("dep");
declare namespace extpkg {
    interface Options {
        pattern: string;
    }
}
interface ExtPkg {
    (list: readonly string[], patterns: string | readonly string[], options?: extpkg.Options): string[];
    dep(pattern: string, options?: dep.Options): string[];
}
declare const extpkg: ExtPkg;
export = extpkg;

// @filename: /node_modules/@types/extpkg/package.json
{ "name": "@types/extpkg", "version": "1.0.0" }

// @filename: /test.cjs
/** @type {typeof import("extpkg") | undefined} */
let extpkg;

module.exports.extpkg = extpkg;
