
// SimUI v5
// (C) LinkiumPlus Software Powered by IT Dept, Zenith Academy


window.su5 = {
	init() {
		document.onkeydown = e => {
			if (e.key == "Tab") e.preventDefault();
		}
		const initNode = node => {
			if (node.classList?.contains("input")) su5.form.init(node);
			if (node.classList?.contains("progress")) node.progress = new su5.progress(node);
			if (node.tagName == "OPTION" && node.closest(".input")) su5.form.init(node.closest(".input"));
		};
		// 弹窗显示时禁止页面滚动
		const updateScrollLock = () => document.documentElement.classList.toggle("simsvScrollLock", !!document.querySelector("dialog.modalContainer[open]"));
		const observer = new MutationObserver(mutations => {
			mutations.forEach((mutation) => {
				mutation.addedNodes.forEach(initNode);
			});
			updateScrollLock();
		});
		document.querySelectorAll(".input,.progress").forEach(initNode);
		updateScrollLock();
		observer.observe(document.body, { childList: true, subtree: true, attributes: true });
		document.body.ongesturestart = document.body.ongesturechange = document.body.ongestureend = e => e.preventDefault();
	},
	utils: {
		loadScript(url) {
			return new Promise(resolve => {
				let isLoaded;
				Array.from(document.getElementsByTagName("script")).forEach(script => {
					if (script.src?.includes(url)) {
						resolve(true);
						isLoaded = true;
					}
				});
				if (isLoaded) return;
				let script = document.createElement("script");
				script.src = url;
				script.onload = () => resolve(true);
				script.onerror = () => {
					resolve(false);
					script.remove();
				}
				document.body.appendChild(script);
			});
		},
		loadStyle(url) {
			return new Promise(resolve => {
				let isLoaded;
				Array.from(document.getElementsByTagName("link")).forEach(link => {
					if (link.rel == "stylesheet" && link.href?.includes(url)) {
						resolve(true);
						isLoaded = true;
					}
				});
				if (isLoaded) return;
				let link = document.createElement("link");
				link.href = url;
				link.rel = "stylesheet";
				link.onload = () => resolve(true);
				link.onerror = () => {
					resolve(false);
					link.remove();
				}
				document.body.appendChild(link);
			});
		},
		createElement(tag = "div", text = "", classes = [], parent, id) {
			const element = document.createElement(tag);
			classes.forEach(className => element.classList.add(className));
			if (text.startsWith("text:")) element.textContent = text.slice(5); else element.innerHTML = text;
			if (parent) parent.appendChild(element);
			if (id) element.id = id;
			return element;
		},
		formatText(text) {
			text = String(text) || "";
			return text.replace(/[&<>"']/g, m => {
				return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#039;" }[m];
			});
		}
	},
	form: {
		init(ele) {
			if (!ele) return;
			// .input 可以是外层包装，也可以直接加在 input / select / textarea 上
			const field = node => node.matches("input, select, textarea") ? node : node.querySelector("input, select, textarea");
			// 判断留空与回车跳转处理
			ele.onkeydown = e => {
				if (e.key == "Enter") {
					if (e.target.tagName == "TEXTAREA" && !e.ctrlKey && !e.shiftKey) return;
					e.preventDefault();
					if (!ele.classList.contains("allowEmpty") && !field(ele).value.trim()) {
						clearTimeout(ele.inputTipTimeout);
						ele.classList.add("warning");
						ele.inputTipTimeout = setTimeout(() => { ele.classList.remove("warning"); }, 200);
					} else if (ele.nextElementSibling?.classList?.contains("input")) {
						const next = field(ele.nextElementSibling);
						next.select ? next.select() : next.focus();
					} else if (ele.nextElementSibling?.tagName.toLowerCase() == "button") {
						ele.nextElementSibling.click();
					} else if (ele.parentElement.onsubmit) {
						ele.parentElement.onsubmit();
					} else if (ele.closest(".modalContainer")) {
						ele.closest(".modalContainer").querySelector("button:not(.sub)").click();
					}
				}
			}
			const inputElement = ele.querySelector("input, select, textarea");
			if (inputElement) {
				ele.onclick = e => {
					if (!e.target.closest("input, select, textarea")) inputElement.focus();
				};
				inputElement.autocomplete = "off";
			}
			// 右侧功能按钮
			const functionBtn = ele.querySelector("i.clickable");
			if (functionBtn) {
				if (!functionBtn.innerHTML) {
					const inputElement = ele.querySelector("input, select");
					if (inputElement) {
						if (inputElement.type == "password") {
							functionBtn.innerHTML = "&#xECB5;";
							functionBtn.onpointerdown = () => {
								functionBtn.innerHTML = "&#xECB7;";
								inputElement.type = "password-visible";
								document.documentElement.onpointerup = () => {
									document.documentElement.onpointerup = null;
									inputElement.type = "password";
									functionBtn.innerHTML = "&#xECB5;";
								}
							};
						}
						else if (inputElement.tagName == "SELECT") {
							functionBtn.innerHTML = "&#xF327;";
						} else {
							functionBtn.innerHTML = "&#xF4C8;";
							functionBtn.onclick = () => {
								inputElement.value = "";
							};
						}
						inputElement.onfocus = inputElement.oninput = () => {
							ele.classList[!inputElement.value ? "add" : "remove"]("empty");
						};
					}
				}
			}
			// 优化下拉框
			const isSafari = /^((?!chrome|android).)*safari/i.test(navigator.userAgent);
			if (isSafari) inputElement.classList.add("safari");
			if (inputElement && inputElement.tagName == "SELECT" && !isSafari) {
				ele.classList.add("select");
				ele.querySelectorAll(".SimsvOptionsContainer").forEach(old => old.remove());
				simCss(inputElement).pointerEvents = "none";
				const options = Array.from(inputElement.querySelectorAll("option")).map(ele => { return { value: ele.value, disabled: ele.disabled, text: ele.textContent }; });
				const optionContainer = su5.utils.createElement("div", "", ["SimsvOptionsContainer", "menu"], inputElement.parentElement);
				options.forEach(option => {
					const optionEle = su5.utils.createElement("div", "text:" + option.text, ["clickable"], optionContainer);
					if (option.disabled) optionEle.classList.add("disabled");
					optionEle.dataset.value = option.value;
					optionEle.onclick = e => {
						e.stopPropagation();
						inputElement.value = option.value;
						inputElement.onchange();
					};
				});
				if (!inputElement.originalOnchange) inputElement.originalOnchange = inputElement.onchange ? inputElement.onchange : () => {};
				const onchange = (_event, isInit) => {
					optionContainer.querySelectorAll("div").forEach(optionEle => {
						optionEle.classList[optionEle.dataset.value == inputElement.value ? "add" : "remove"]("active");
					});
					su5.dropDown.hide(optionContainer);
					if (!isInit && inputElement.originalOnchange) inputElement.originalOnchange();
				};
				// 原生 change 事件仍然走 onchange；之后页面对 onchange 的赋值改为替换 originalOnchange
				Object.getOwnPropertyDescriptor(HTMLElement.prototype, "onchange").set.call(inputElement, onchange);
				Object.defineProperty(inputElement, "onchange", {configurable: true, get: () => onchange, set: handler => inputElement.originalOnchange = handler || (() => {})});
				su5.dropDown.create(optionContainer, ele);
				inputElement.onchange(false, true);
			}
		},
		getValue(id, options = {}) {
			const form = document.getElementById(id) || su5.dialog.find?.(id);
			const json = {};
			for (const input of form.querySelectorAll("input, select, textarea")) {
				// 检查是否留空
				const ele = input.parentElement;
				if (!options.skipCheck && !ele.classList.contains("allowEmpty") && !input.value.trim()) {
					clearTimeout(ele.inputTipTimeout);
					ele.classList.add("warning");
					ele.inputTipTimeout = setTimeout(() => { ele.classList.remove("warning"); }, 200);
					input.focus();
					return false;
				}
				// 添加数据
				const inputName = input.name || input.id;
				if (!inputName || json[inputName]) continue;
				json[inputName] = input.type == "password" ? md5(input.value) : input.value;
			};
			return json;
		}
	},
	dialog: {
		create(title = "提示", subtitle = "") {
			const dialogId = "SimsvBaseDialog-" + new Date().getTime() + Math.round(Math.random() * 106);
			if (document.getElementById(dialogId)) return su5.dialog.create(title, subtitle);
			const outerContainer = su5.utils.createElement("dialog", "", ["modalContainer"], document.body, dialogId);
			const container = su5.utils.createElement("div", "", [], outerContainer);
			const modalHeader = su5.utils.createElement("div", "", ["modalHeader"], container);
			su5.utils.createElement("div", "text:" + title, [], modalHeader);
			su5.utils.createElement("small", "text:" + subtitle, [], modalHeader);
			const modalContent = su5.utils.createElement("div", "", ["modalContent"], container);
			return { id: dialogId, content: modalContent, container };
		},
		show(id, isShow = true, isModal = true) {
			const dialog = document.getElementById(id);
			if (isShow) {
				setTimeout(() => dialog[isModal ? "showModal" : "show"](), 50);
				dialog.oncancel = e => {
					// 只在底部按钮行里找（.buttons，或内容区最后一个元素），免得点到内容里的按钮（比如字段旁的选择按钮）；都没有按钮时才在整个弹窗里找
					const footer = [dialog.querySelector(".buttons"), dialog.querySelector(".modalContent > :last-child")].find(ele => ele?.querySelector("button")) || dialog;
					const cancelBtn = footer.querySelector("button.sub");
					if (cancelBtn) return cancelBtn.click();
					const submitBtn = footer.querySelector("button");
					if (submitBtn) return submitBtn.click();
					e.preventDefault();
				};
				dialog.onkeydown = e => {
					if (e.key == "Escape") {
						dialog.oncancel();
						e.preventDefault();
					}
				};
			} else {
				dialog.close();
			}
		},
		alert(config = {}) {
			return new Promise(resolve => {
				if (typeof config == "string") config = { text: config };
				const { id, content, container } = su5.dialog.create(config.title ?? "提示", config.subtitle ?? "");
				content[config.text.startsWith("html:") ? "innerHTML" : "innerText"] = config.text.replace("html:", "");
				const buttonGroup = su5.utils.createElement("div", "", ["buttons"], content);
				const submitButton = su5.utils.createElement("button", config.buttonHtml ?? "<i>&#xEB7B;</i> 确定", [], buttonGroup);
				submitButton.onclick = () => {
					resolve(true);
					su5.dialog.show(id, false);
				};
				su5.dialog.show(id);
			});
		},
		confirm(config = {}) {
			return new Promise(resolve => {
				if (typeof config == "string") config = { text: config };
				const { id, content, container } = su5.dialog.create(config.title ?? "提示", config.subtitle ?? "");
				content[config.text.startsWith("html:") ? "innerHTML" : "innerText"] = config.text.replace("html:", "");
				const buttonGroup = su5.utils.createElement("div", "", ["buttons"], content);
				const cancelButton = su5.utils.createElement("button", config.cancelHtml ?? "取消", ["sub", "cancel"], buttonGroup);
				cancelButton.onclick = () => {
					resolve(false);
					su5.dialog.show(id, false);
				};
				const submitButton = su5.utils.createElement("button", config.buttonHtml ?? "<i>&#xEB7B;</i> 确定", [], buttonGroup);
				submitButton.onclick = () => {
					resolve(true);
					su5.dialog.show(id, false);
				};
				su5.dialog.show(id);
			});
		},
		prompt(config = {}, value = "") {
			return new Promise(resolve => {
				if (typeof config == "string") config = { items: [{ type: "input", placeholder: config, value, name: "value" }] };
				const { id, content, container } = su5.dialog.create(config.title ?? "提示", config.subtitle ?? "");
				const inputForm = su5.utils.createElement("form", "", [], content, id + "-form");
				config.items.forEach(item => {
					const inputDiv = su5.utils.createElement("div", "", item.allowEmpty ? ["input", "allowEmpty"] : ["input"], inputForm);
					if (item.icon) su5.utils.createElement("i", item.icon, [], inputDiv);
					let input;
					switch (item.type) {
						case "select":
							input = su5.utils.createElement("select", `<option value="" disabled>请选择 ...</option>`, [], inputDiv);
							if (item.placeholder) input.querySelector("option").textContent = item.placeholder;
							for (const value in item.options) {
								const option = su5.utils.createElement("option", "text:" + item.options[value], [], input);
								option.value = value;
							}
							su5.utils.createElement("i", "", ["clickable"], inputDiv);
							break;
						case "textarea":
							input = su5.utils.createElement("textarea", "", [], inputDiv);
							input.placeholder = item.placeholder ?? "";
							break;
						default:
							input = su5.utils.createElement("input", "", [], inputDiv);
							input.type = item.inputType ?? "text";
							input.placeholder = item.placeholder ?? "";
							if (["text", "password"].includes(input.type)) su5.utils.createElement("i", "", ["clickable"], inputDiv);
					}
					input.name = item.name;
					input.value = item.value ?? "";
				});
				const buttonGroup = su5.utils.createElement("div", "", ["buttons"], content);
				const cancelButton = su5.utils.createElement("button", config.cancelHtml ?? "取消", ["sub", "cancel"], buttonGroup);
				cancelButton.onclick = () => {
					resolve(false);
					su5.dialog.show(id, false);
				};
				const submitButton = su5.utils.createElement("button", config.buttonHtml ?? "<i>&#xEB7B;</i> 确定", [], buttonGroup);
				submitButton.onclick = () => {
					const value = su5.form.getValue(inputForm.id);
					if (!value) return;
					resolve(value);
					su5.dialog.show(id, false);
				};
				inputForm.onsubmit = submitButton.onclick;
				su5.dialog.show(id);
			});
		}
	},
	toast(text, options = {}) {
		let toastContainer = document.getElementById("SimsvToastContainer");
		if (!toastContainer) toastContainer = su5.utils.createElement("div", "", [], document.body, "SimsvToastContainer");
		const toastElement = su5.utils.createElement("div", "", [], toastContainer);
		if (options.icon) su5.utils.createElement("i", options.icon, [], toastElement);
		su5.utils.createElement("span", "text:" + text, [], toastElement);
		let hidden;
		const hideToast = () => {
			if (hidden) return;
			hidden = true;
			simCss(toastElement).setProperty("--clientHeight", `${toastElement.clientHeight}px`);
			toastElement.classList.add("inactive");
			setTimeout(() => toastElement.classList.add("heightFade"), 200);
			setTimeout(() => {
				toastElement.remove();
				if (options.onhide) options.onhide(text);
			}, 300);
		}
		setTimeout(hideToast, options.timeout ?? 3000);
		toastElement.onclick = () => {
			hideToast();
			if (options.onclick) options.onclick(text);
		}
	},
	progress: class {
		#ele;
		#input;
		#dragging = false;
		constructor(element) {
			// 初始化
			element.classList.add("progress");
			element.innerHTML = `<div class="outerBar"><div class="bar"></div></div><div class="dot"></div><input type="range" step="0.01">`;
			this.#ele = element;
			this.#input = element.querySelector("input");
			// 事件监听
			this.#input.oninput = () => {
				this.#dragging = true;
				this.triggerChange();
			};
			this.#input.onchange = () => {
				this.#dragging = false;
				if (this.onchange) this.onchange(this.value);
			};
			// 拖动可能等不到 change（松开前窗口失去焦点、鼠标捕获被其他窗口取走）：此时也结束拖动状态，否则之后的赋值会一直被忽略
			const endDrag = () => { this.#dragging = false; };
			["pointerup", "pointercancel", "blur"].forEach(type => this.#input.addEventListener(type, endDrag));
			window.addEventListener("blur", endDrag);
			// 读取信息
			this.min = Number(element.getAttribute("min")) ?? options.min ?? parseFloat(element.getAttribute("min")) ?? 0;
			this.max = Number(element.getAttribute("max")) ?? options.max ?? parseFloat(element.getAttribute("max")) ?? this.min + 100;
			this.value = Number(element.getAttribute("value")) ?? this.min;
		}
		triggerChange(manualTrigger) {
			const ratio = (this.value - this.min) / (this.max - this.min);
			const bar = this.#ele.querySelector(".bar");
			const dot = this.#ele.querySelector(".dot");
			simCss(bar).transition = simCss(dot).transition = manualTrigger ? "all .2s" : "transform .2s";
			simCss(bar).width = `calc(6px + ${ratio} * (100% - 12px))`
			simCss(dot).left = `calc(${ratio} * (100% - 12px))`
			this.#input.value = this.value;
			if (!manualTrigger && this.ondrag) this.ondrag(this.value);
		}
		set value(newValue) {
			if (this.#dragging) return;
			this.#input.value = Math.min(this.max, Math.max(this.min, newValue));
			this.triggerChange(true);
		}
		get value() { return parseFloat(this.#input.value); }
		set min(newValue) {
			this.#input.min = newValue;
			this.triggerChange(true);
		}
		get min() { return this.#input.min; }
		set max(newValue) {
			this.#input.max = newValue;
			this.triggerChange(true);
		}
		get max() { return this.#input.max; }
		setValue(newValue) {
			this.value = newValue;
		}
	},
	dropDown: {
		activeDropdown: null,
		initialized: false,
		initGlobalListeners(doc = document) {
			if (doc.simsvDropdownListeners) return;
			doc.simsvDropdownListeners = true;
			const win = doc.defaultView;
			const tryHideDropdown = e => doc.querySelectorAll(".dropdown.active").forEach(dropdown => {
				if (!dropdown.contains(e.target) && !dropdown.trigger?.contains(e.target)) this.hide(dropdown);
			});
			doc.addEventListener("pointerdown", tryHideDropdown);
			doc.addEventListener("keydown", e => { if (e.key == "Escape") tryHideDropdown(e); });
			win.addEventListener("wheel", tryHideDropdown);
			const directHideDropdown = () => doc.querySelectorAll(".dropdown.active").forEach(dropdown => this.hide(dropdown));
			win.addEventListener("blur", directHideDropdown);
			win.addEventListener("scroll", directHideDropdown);
			this.initialized = true;
		},
		create(dropdown, trigger) {
			this.initGlobalListeners(dropdown.ownerDocument);
			dropdown.trigger = trigger;
			dropdown.classList.add("dropdown");
			trigger?.addEventListener("click", () => {
				if (dropdown.classList.contains("active")) this.hide(dropdown);
				else this.show(dropdown);
			});
		},
		show(dropdown, x, y) {
			if (dropdown.classList.contains("active")) return;
			// 位置按下拉框所在的页面计算
			const document = dropdown.ownerDocument, window = document.defaultView;
			document.querySelectorAll(".dropdown.active").forEach(ele => this.hide(ele));
			this.activeDropdown = dropdown;
			if (!Number.isFinite(x) || !Number.isFinite(y)) {
				const trigger = dropdown.trigger;
				const rect = trigger.getBoundingClientRect();
				trigger.classList.add("dropdownActive");
				// 下拉框选项列表与输入框等宽，弹窗外由 CSS 覆盖
				if (dropdown.classList.contains("SimsvOptionsContainer")) simCss(dropdown).width = `${rect.width}px`;
				const heightOverflow = rect.top + trigger.offsetHeight + dropdown.clientHeight > document.documentElement.clientHeight;
				dropdown.classList[heightOverflow ? "add" : "remove"]("bottom");
				simCss(dropdown).top = `${heightOverflow ? (rect.top - dropdown.clientHeight - 5) : (rect.top + trigger.offsetHeight + 5)}px`;
				simCss(dropdown).left = `${rect.left + dropdown.clientWidth > document.documentElement.clientWidth ? rect.right - dropdown.clientWidth : rect.left}px`;
			} else {
				const heightOverflow = y + dropdown.clientHeight > document.documentElement.clientHeight;
				dropdown.classList[heightOverflow ? "add" : "remove"]("bottom");
				simCss(dropdown).top = `${heightOverflow ? (y - dropdown.clientHeight - 5) : (y + 5)}px`;
				simCss(dropdown).left = `${x + dropdown.clientWidth > document.documentElement.clientWidth ? document.documentElement.clientWidth - dropdown.clientWidth : x}px`;
			}
			dropdown.classList.add("active");
			window.focus();
		},
		hide(dropdown) {
			if (this.activeDropdown === dropdown) this.activeDropdown = null;
			if (!dropdown.classList.contains("active")) return;
			dropdown.classList.remove("active");
			dropdown.trigger?.classList.remove("dropdownActive");
			dropdown.querySelectorAll("*").forEach(ele => ele.blur());
		}
	},
	renderPagination(data, searchInput, paginationContainer, callback) {
		paginationContainer.classList.add("pagination");
		paginationContainer.innerHTML = "";
		const disableAllButtons = () => {
			const buttons = paginationContainer.querySelectorAll("button");
			buttons.forEach(btn => btn.disabled = true);
		}
		if (searchInput) searchInput.onkeydown = (e) => {
			if (e.key === "Enter") {
				disableAllButtons();
				callback(1, searchInput.value);
			}
		};
		if (data.pagination.total === 0 || data.pagination.pages <= 1) return;
		const totalPages = data.pagination.pages;
		const currentPage = data.pagination.current;
		const maxButtons = 5;
		const prevBtn = document.createElement("button");
		prevBtn.innerHTML = "<i></i>";
		prevBtn.disabled = currentPage === 1;
		prevBtn.onclick = () => {
			if (currentPage > 1) {
				disableAllButtons();
				callback(currentPage - 1, searchInput?.value);
			}
		};
		paginationContainer.appendChild(prevBtn);
		let startPage, endPage;
		if (totalPages <= maxButtons) {
			startPage = 1;
			endPage = totalPages;
		} else {
			const half = Math.floor(maxButtons / 2);
			if (currentPage <= half + 1) {
				startPage = 1;
				endPage = maxButtons;
			} else if (currentPage >= totalPages - half) {
				startPage = totalPages - maxButtons + 1;
				endPage = totalPages;
			} else {
				startPage = currentPage - half;
				endPage = currentPage + half;
			}
		}
		for (let i = startPage; i <= endPage; i++) {
			const pageBtn = document.createElement("button");
			pageBtn.className = "sub";
			pageBtn.textContent = i;
			pageBtn.disabled = i === currentPage;
			pageBtn.onclick = () => {
				disableAllButtons();
				callback(i, searchInput?.value);
			};
			paginationContainer.appendChild(pageBtn);
		}
		const nextBtn = document.createElement("button");
		nextBtn.innerHTML = "<i></i>";
		nextBtn.disabled = currentPage === totalPages;
		nextBtn.onclick = () => {
			if (currentPage < totalPages) {
				disableAllButtons();
				callback(currentPage + 1, searchInput?.value);
			}
		};
		paginationContainer.appendChild(nextBtn);
	}
};

su5.init();



