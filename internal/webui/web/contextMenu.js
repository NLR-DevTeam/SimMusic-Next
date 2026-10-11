const simTrackMenu = (() => {
    let generation = 0;
    const glyph = code => menuIcon(code);
    const positionChild = (menu, button) => {
        const anchor = button.getBoundingClientRect();
        const bounds = menu.getBoundingClientRect();
        simCss(menu).left = `${Math.max(4, Math.min(innerWidth - bounds.width - 4, anchor.right + bounds.width < innerWidth - 4 ? anchor.right - 2 : anchor.left - bounds.width + 2))}px`;
        simCss(menu).top = `${Math.max(4, Math.min(innerHeight - bounds.height - 4, anchor.top - 5))}px`;
    };
    const closeChild = menu => { menu?.classList.remove("active"); menu?.ownerButton?.classList.remove("active"); if (menu?.openChild) closeChild(menu.openChild); };
    const fill = (menu, items, token) => {
        menu.replaceChildren();
        menu.classList.toggle("noIcons", !items.some(item => item.icon));
        items.forEach(item => {
            if (item.separator) { const line = document.createElement("div"); line.className = "contextSeparator"; menu.append(line); return; }
            const row = document.createElement("div");
            row.className = "contextItem";
            const button = document.createElement("button");
            button.className = "clickable";
            const symbol = document.createElement("i");
            symbol.textContent = glyph(item.icon);
            const label = document.createElement("span");
            label.textContent = item.label;
            button.append(symbol, label);
            row.append(button);
            menu.append(row);
            if (!item.children) { button.onclick = () => { su5.dropDown.hide(contextMenu); item.action?.(); }; return; }
            const arrow = document.createElement("i");
            arrow.className = "contextArrow";
            arrow.textContent = glyph("EA6E");
            button.append(arrow);
            const child = document.createElement("div");
            child.className = "contextSubmenu";
            child.ownerButton = button;
            contextMenu.append(child);
            let closeTimer;
            let loaded = false;
            const open = async () => {
                clearTimeout(closeTimer);
                if (!loaded) {
                    loaded = true;
                    fill(child, [{ label: "正在读取…" }], token);
                    child.classList.add("active");
                    positionChild(child, button);
                    try {
                        const items = typeof item.children === "function" ? await item.children() : item.children;
                        if (generation !== token) return;
                        fill(child, items?.length ? items : [{ label: "暂无可用操作" }], token);
                    } catch (error) { loaded = false; fill(child, [{ label: error.message }], token); }
                }
                if (generation !== token) return;
                if (menu.openChild !== child) closeChild(menu.openChild);
                menu.openChild = child;
                button.classList.add("active");
                child.classList.add("active");
                positionChild(child, button);
            };
            button.onclick = open;
            row.onpointerenter = open;
            row.onpointerleave = () => closeTimer = setTimeout(() => closeChild(child), 220);
            child.onpointerenter = () => { clearTimeout(closeTimer); if (menu !== contextMenu) menu.onpointerenter?.(); };
            child.onpointerleave = () => closeTimer = setTimeout(() => closeChild(child), 220);
        });
    };
    return { show: (items, x, y) => {
        const token = ++generation;
        su5.dropDown.hide(contextMenu);
        fill(contextMenu, items, token);
        contextMenu.openChild = null;
        su5.dropDown.create(contextMenu);
        su5.dropDown.show(contextMenu, Math.max(1, x), Math.max(1, y));
        const bounds = contextMenu.getBoundingClientRect();
        simCss(contextMenu).left = `${Math.max(4, Math.min(innerWidth - bounds.width - 4, parseFloat(simCss(contextMenu).left)))}px`;
        simCss(contextMenu).top = `${Math.max(4, Math.min(innerHeight - bounds.height - 4, parseFloat(simCss(contextMenu).top)))}px`;
    } };
})();
