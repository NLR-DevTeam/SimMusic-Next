// SimUI uses CSSOM declarations from an external sheet for dynamic geometry.
const cssDeclarations = new WeakMap();
let cssSequence = 0;
window.simCss = element => {
    const cached = cssDeclarations.get(element);
    if (cached?.parentRule.parentStyleSheet) return cached;
    const className = `simGeometry${++cssSequence}`;
    element.classList.add(className);
    const sheet = runtimeCss.sheet;
    // Match the precedence of the original element.style without inline styles.
    // A plain class loses to SimUI's compound selectors, including drag transitions.
    const selector = `:is(#simRuntimePriority#simRuntimePriority:not(*), .${className})`;
    const index = sheet.insertRule(`${selector} {}`, sheet.cssRules.length);
    const declaration = sheet.cssRules[index].style;
    cssDeclarations.set(element, declaration);
    return declaration;
};
setInterval(() => {
    const sheet = runtimeCss.sheet;
    for (let index = sheet.cssRules.length - 1; index >= 0; index--)
        if (!document.querySelector(sheet.cssRules[index].selectorText)) sheet.deleteRule(index);
}, 30000);
