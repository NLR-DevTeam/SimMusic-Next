// SimUI uses CSSOM declarations from an external sheet for dynamic geometry.
// The sheet is that of the document the element is in: a dialog moves to a window of its own.
const cssDeclarations = new WeakMap();
let cssSequence = 0;
window.simCss = element => {
    const sheet = element.ownerDocument.getElementById("runtimeCss").sheet;
    const cached = cssDeclarations.get(element);
    if (cached?.parentRule.parentStyleSheet === sheet) return cached;
    const className = `simGeometry${++cssSequence}`;
    element.classList.add(className);
    // Match the precedence of the original element.style without inline styles.
    // A plain class loses to SimUI's compound selectors, including drag transitions.
    const selector = `:is(#simRuntimePriority#simRuntimePriority:not(*), .${className})`;
    const index = sheet.insertRule(`${selector} {}`, sheet.cssRules.length);
    const declaration = sheet.cssRules[index].style;
    // What was declared for the element in the document it came from comes with it.
    if (cached?.parentRule.parentStyleSheet) declaration.cssText = cached.cssText;
    cssDeclarations.set(element, declaration);
    return declaration;
};
// Carries the declarations of an element and everything in it over to the document they are in now.
window.simCssAdopt = root => [root, ...root.querySelectorAll("[class*=simGeometry]")].forEach(element => { if (cssDeclarations.has(element)) simCss(element); });
setInterval(() => {
    const sheet = runtimeCss.sheet;
    for (let index = sheet.cssRules.length - 1; index >= 0; index--)
        if (!document.querySelector(sheet.cssRules[index].selectorText)) sheet.deleteRule(index);
}, 30000);
