//// [tests/cases/compiler/cjsModuleExportsNamedDeclarationEmit.ts] ////

//// [helper.js]
class InternalClass {
    /** @param {string} x */
    constructor(x) {
        this.x = x;
    }
}
module.exports = { InternalClass };

//// [index.js]
/** @type {typeof import("./helper").InternalClass} */
const Cls = require("./helper").InternalClass;
exports.instance = new Cls("hello");


//// [helper.js]
"use strict";
class InternalClass {
    /** @param {string} x */
    constructor(x) {
        this.x = x;
    }
}
module.exports = { InternalClass };
//// [index.js]
"use strict";
/** @type {typeof import("./helper").InternalClass} */
const Cls = require("./helper").InternalClass;
exports.instance = new Cls("hello");


//// [helper.d.ts]
declare class InternalClass {
    /** @param {string} x */
    constructor(x: string);
}
declare const _default: {
    InternalClass: typeof InternalClass;
};
export = _default;
//// [index.d.ts]
export declare var instance: {
    x: string;
};
