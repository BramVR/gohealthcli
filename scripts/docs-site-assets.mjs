export function css() {
  return `
:root[data-theme="dark"]{
  --ink:#f1f5f9;
  --text:#cbd5e1;
  --muted:#94a3b8;
  --subtle:#64748b;
  --bg:#0a0f1a;
  --paper:#11182a;
  --paper-2:#1a2436;
  --accent:#14b8a6;
  --accent-soft:rgba(20,184,166,.14);
  --accent-strong:#2dd4bf;
  --warn:#fbbf24;
  --warn-soft:rgba(251,191,36,.14);
  --line:#1e293b;
  --line-soft:#172033;
  --code-bg:#060a14;
  --code-fg:#e2e8f0;
  --code-inline-fg:#e2e8f0;
  --code-border:#1e293b;
  --pill-border:#334155;
  --shadow-card:0 8px 28px rgba(0,0,0,.4);
  --scrollbar:#334155;
  --hl-keyword:#5eead4;
  --hl-string:#4ade80;
  --hl-number:#fbbf24;
  --hl-comment:#64748b;
  --hl-flag:#a78bfa;
  --hl-meta:#2dd4bf;
  --hl-prompt:#4ade80;
  --grain:url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='160' height='160'><filter id='n'><feTurbulence type='fractalNoise' baseFrequency='.85' numOctaves='2' stitchTiles='stitch'/><feColorMatrix values='0 0 0 0 .55 0 0 0 0 .75 0 0 0 0 .85 0 0 0 .025 0'/></filter><rect width='100%25' height='100%25' filter='url(%23n)'/></svg>");
}
:root,:root[data-theme="light"]{
  --ink:#0f172a;
  --text:#334155;
  --muted:#64748b;
  --subtle:#94a3b8;
  --bg:#f6f9fc;
  --paper:#ffffff;
  --paper-2:#eef4fa;
  --accent:#0d9488;
  --accent-soft:rgba(13,148,136,.10);
  --accent-strong:#0f766e;
  --warn:#b45309;
  --warn-soft:rgba(180,83,9,.10);
  --line:#e2e8f0;
  --line-soft:#f1f5f9;
  --code-bg:#0f172a;
  --code-fg:#e2e8f0;
  --code-inline-fg:#0f766e;
  --code-border:#1e293b;
  --pill-border:#cbd5e1;
  --shadow-card:0 6px 24px rgba(15,23,42,.06);
  --scrollbar:#cbd5e1;
  --hl-keyword:#5eead4;
  --hl-string:#4ade80;
  --hl-number:#fbbf24;
  --hl-comment:#94a3b8;
  --hl-flag:#a78bfa;
  --hl-meta:#2dd4bf;
  --hl-prompt:#4ade80;
  --grain:none;
}
:root,:root[data-theme="light"]{color-scheme:light}
:root[data-theme="dark"]{color-scheme:dark}
*{box-sizing:border-box}
html{scroll-behavior:smooth;scroll-padding-top:32px}
body{margin:0;background:var(--bg);color:var(--text);font-family:"Inter",ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif;line-height:1.65;overflow-x:hidden;-webkit-font-smoothing:antialiased;font-feature-settings:"cv02","cv03","cv04","cv11","ss01";transition:background-color .2s,color .2s}
body::before{content:"";position:fixed;inset:0;pointer-events:none;background-image:var(--grain);background-repeat:repeat;opacity:.5;z-index:0;mix-blend-mode:overlay}
::selection{background:var(--accent);color:#fff}
a{color:var(--accent);text-decoration:none;transition:color .12s}
a:hover{text-decoration:underline;text-underline-offset:.22em;text-decoration-thickness:1px}
.shell{position:relative;z-index:1;display:grid;grid-template-columns:248px minmax(0,1fr);min-height:100vh}
.sidebar{position:sticky;top:0;height:100vh;overflow:auto;padding:28px 22px 22px;background:var(--bg);border-right:1px solid var(--line);scrollbar-width:thin;scrollbar-color:var(--line) transparent;transition:background-color .2s,border-color .2s}
.sidebar::-webkit-scrollbar{width:6px}
.sidebar::-webkit-scrollbar-thumb{background:var(--line);border-radius:6px}
.sidebar-head{display:flex;align-items:center;gap:10px;margin-bottom:28px}
.brand{display:flex;align-items:center;gap:11px;color:var(--ink);text-decoration:none;flex:1;min-width:0}
.brand:hover{text-decoration:none}
.brand .mark{flex:0 0 28px;width:28px;height:28px;border-radius:7px;background:linear-gradient(135deg,#1e293b,#0a1018);display:flex;align-items:center;justify-content:center;color:var(--accent);border:1px solid var(--line)}
:root[data-theme="light"] .brand .mark{background:linear-gradient(135deg,#ffffff,#e6f4f2);color:var(--accent)}
.brand .mark svg{width:18px;height:18px;display:block}
.brand strong{display:block;font-family:"Fraunces","Times New Roman",ui-serif,serif;font-size:1.08rem;line-height:1.05;font-weight:600;letter-spacing:-.005em;color:var(--ink);font-variation-settings:"SOFT" 30,"opsz" 14}
.brand small{display:block;color:var(--muted);font-size:.7rem;margin-top:3px;font-weight:500;letter-spacing:.04em;text-transform:uppercase}
.theme-toggle{display:inline-flex;align-items:center;justify-content:center;flex:0 0 auto;width:34px;height:34px;border-radius:8px;border:1px solid var(--line);background:transparent;color:var(--muted);cursor:pointer;padding:0;transition:border-color .15s,color .15s,background-color .18s,transform .12s}
.theme-toggle:hover{border-color:var(--accent);color:var(--accent)}
.theme-toggle:active{transform:scale(.94)}
.theme-toggle svg{width:15px;height:15px}
.theme-toggle .theme-icon-sun{display:none}
.theme-toggle .theme-icon-moon{display:block}
:root[data-theme="dark"] .theme-toggle .theme-icon-sun{display:block}
:root[data-theme="dark"] .theme-toggle .theme-icon-moon{display:none}
.search{display:block;margin:0 0 24px}
.search span{display:block;color:var(--muted);font-size:.66rem;font-weight:600;text-transform:uppercase;letter-spacing:.08em;margin-bottom:8px}
.search input{width:100%;border:1px solid var(--line);background:var(--paper);border-radius:8px;padding:9px 12px;font:inherit;font-size:.88rem;color:var(--text);outline:none;transition:border-color .15s,box-shadow .15s,background-color .18s}
.search input::placeholder{color:var(--subtle)}
.search input:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-soft)}
nav section{margin:0 0 20px}
nav h2{font-size:.66rem;color:var(--subtle);text-transform:uppercase;letter-spacing:.1em;margin:0 0 8px;font-weight:600}
.nav-link{display:block;color:var(--text);text-decoration:none;border-radius:6px;padding:5px 10px;margin:1px 0;font-size:.88rem;line-height:1.4;transition:background .12s,color .12s}
.nav-link:hover{background:var(--line-soft);color:var(--ink);text-decoration:none}
.nav-link.active{background:var(--accent-soft);color:var(--accent);font-weight:600}
main{min-width:0;padding:48px clamp(22px,5vw,72px) 96px;max-width:1180px;margin:0 auto;width:100%}
.hero{display:flex;align-items:flex-end;justify-content:space-between;gap:24px;border-bottom:1px solid var(--line);padding:8px 0 26px;margin-bottom:8px;flex-wrap:wrap}
.hero-text{min-width:0;flex:1 1 320px}
.eyebrow{margin:0 0 10px;color:var(--accent);font-weight:600;text-transform:uppercase;letter-spacing:.1em;font-size:.7rem;display:inline-flex;align-items:center;gap:8px}
.eyebrow::before{content:"";display:inline-block;width:18px;height:1px;background:var(--accent)}
.hero h1{font-family:"Fraunces","Times New Roman",ui-serif,serif;font-size:2.2rem;line-height:1.08;letter-spacing:-.02em;margin:0;font-weight:600;color:var(--ink);font-variation-settings:"SOFT" 30,"opsz" 96}
.hero-meta{display:flex;gap:8px;flex:0 0 auto;flex-wrap:wrap}
.repo,.edit,.btn-ghost{border:1px solid var(--line);color:var(--text);text-decoration:none;border-radius:7px;padding:7px 12px;font-weight:500;font-size:.83rem;background:transparent;transition:border-color .15s,color .15s,background .15s}
.repo:hover,.edit:hover,.btn-ghost:hover{border-color:var(--accent);color:var(--accent);text-decoration:none;background:var(--accent-soft)}
.edit{color:var(--muted)}
.home-hero{padding:32px 0 40px;margin-bottom:8px;border-bottom:1px solid var(--line);display:grid;grid-template-columns:minmax(0,1.1fr) minmax(0,1fr);gap:48px;align-items:center}
.home-hero .hero-text{min-width:0}
.hero-art{position:relative;display:flex;align-items:center;justify-content:center;min-height:200px}
.hero-art svg{width:100%;max-width:460px;height:auto;border-radius:16px;filter:drop-shadow(0 22px 44px rgba(15,23,42,.18))}
:root[data-theme="dark"] .hero-art svg{filter:drop-shadow(0 22px 44px rgba(0,0,0,.5))}
.ekg-sweep{stroke-dasharray:120 1500;animation:ekg-sweep 3.4s linear infinite}
@keyframes ekg-sweep{from{stroke-dashoffset:120}to{stroke-dashoffset:-1500}}
.ekg-dot{transform-origin:center;transform-box:fill-box;animation:ekg-pulse 1.05s ease-in-out infinite}
@keyframes ekg-pulse{0%,100%{opacity:.5;transform:scale(.85)}50%{opacity:1;transform:scale(1.35)}}
@media(prefers-reduced-motion:reduce){.ekg-sweep{animation:none;stroke-dasharray:none}.ekg-dot{animation:none}}
.home-hero h1{font-family:"Fraunces","Times New Roman",ui-serif,serif;font-size:clamp(2.4rem,4.4vw,3.6rem);line-height:1.02;letter-spacing:-.022em;margin:0 0 .42em;font-weight:500;color:var(--ink);font-variation-settings:"SOFT" 40,"opsz" 144;max-width:16ch}
.home-hero h1 .accent{color:var(--accent);font-style:italic;font-variation-settings:"SOFT" 40,"opsz" 144}
.home-hero .lede{font-size:1.16rem;line-height:1.55;color:var(--text);margin:0 0 1.4em;max-width:54ch}
.home-cta{display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin:0 0 12px}
.home-cta .btn{display:inline-flex;align-items:center;gap:7px;border-radius:10px;padding:12px 20px;font-weight:600;font-size:.95rem;text-decoration:none;transition:background .15s,border-color .15s,color .15s,transform .12s,box-shadow .15s}
.home-cta .btn-primary{background:var(--ink);color:var(--bg);border:1px solid var(--ink)}
.home-cta .btn-primary:hover{background:var(--accent-strong);border-color:var(--accent-strong);color:var(--bg);text-decoration:none;transform:translateY(-1px);box-shadow:0 8px 22px var(--accent-soft)}
.home-cta .btn-ghost{border:1px solid var(--ink);color:var(--ink);padding:12px 20px;background:transparent}
.home-cta .btn-ghost:hover{border-color:var(--accent);color:var(--accent);background:var(--accent-soft);transform:translateY(-1px)}
.home-cta .btn-link{border:0;color:var(--muted);padding:12px 8px;background:transparent;font-weight:500}
.home-cta .btn-link:hover{color:var(--accent);background:transparent;text-decoration:none}
.cta-foot{margin:0 0 28px;color:var(--muted);font-size:.86rem;font-style:italic;letter-spacing:.005em}
.home-services{grid-column:1/-1;display:flex;flex-wrap:wrap;gap:8px;margin:18px 0 0;padding-top:6px}
.home-services .cap{display:inline-flex;align-items:center;gap:7px;padding:6px 13px 6px 10px;border:1px solid var(--pill-border);border-radius:999px;font-size:.78rem;font-weight:500;color:var(--text);background:var(--paper);letter-spacing:.005em;transition:border-color .15s,color .15s,background-color .15s}
.home-services .cap:hover{border-color:var(--accent);color:var(--accent);background:var(--accent-soft)}
.home-services .cap-icon{width:16px;height:16px;flex:0 0 16px;color:var(--accent);transition:color .15s}
.home-services .cap:hover .cap-icon{color:var(--accent-strong)}
.doc-grid{display:grid;grid-template-columns:minmax(0,1fr);gap:48px;margin-top:28px}
.doc-grid-home{margin-top:8px}
@media(min-width:1180px){.doc-grid{grid-template-columns:minmax(0,72ch) 200px;justify-content:start}.doc-grid-home{grid-template-columns:minmax(0,76ch);justify-content:start}}
.doc{min-width:0;max-width:72ch;overflow-wrap:break-word}
.doc-home{max-width:76ch}
.doc h1{font-family:"Fraunces","Times New Roman",ui-serif,serif;font-size:2.6rem;line-height:1.08;letter-spacing:-.018em;margin:0 0 .45em;font-weight:600;color:var(--ink)}
body:not(.home) .doc>h1:first-child{display:none}
.doc h2{font-family:"Fraunces","Times New Roman",ui-serif,serif;font-size:1.55rem;line-height:1.2;margin:2.2em 0 .55em;font-weight:600;letter-spacing:-.012em;color:var(--ink);position:relative}
.doc h3{font-size:1.08rem;margin:1.8em 0 .35em;position:relative;font-weight:600;color:var(--ink);letter-spacing:-.005em}
.doc h4{font-size:.95rem;margin:1.5em 0 .25em;color:var(--ink);position:relative;font-weight:600}
.doc h2:first-child,.doc h3:first-child,.doc h4:first-child{margin-top:.2em}
.doc :is(h2,h3,h4) .anchor{position:absolute;left:-1.1em;top:0;color:var(--subtle);opacity:0;text-decoration:none;font-weight:400;padding-right:.3em;transition:opacity .12s,color .12s}
.doc :is(h2,h3,h4):hover .anchor{opacity:.7}
.doc :is(h2,h3,h4) .anchor:hover{opacity:1;color:var(--accent);text-decoration:none}
.doc p{margin:0 0 1.1em}
.doc ul,.doc ol{padding-left:1.3rem;margin:0 0 1.2em}
.doc li{margin:.3em 0}
.doc li>p{margin:0 0 .4em}
.doc strong{font-weight:600;color:var(--ink)}
.doc em{font-style:italic}
.doc code{font-family:"JetBrains Mono","SF Mono",ui-monospace,monospace;font-size:.84em;background:var(--line-soft);border:1px solid var(--line);border-radius:5px;padding:.08em .36em;color:var(--code-inline-fg)}
.doc pre{position:relative;overflow:auto;background:var(--code-bg);color:var(--code-fg);border-radius:9px;padding:14px 18px;margin:1.4em 0;font-size:.86em;line-height:1.6;scrollbar-width:thin;scrollbar-color:#334155 transparent;border:1px solid var(--code-border)}
.doc pre::-webkit-scrollbar{height:8px;width:8px}
.doc pre::-webkit-scrollbar-thumb{background:#334155;border-radius:8px}
.doc pre code{display:block;background:transparent;border:0;color:inherit;padding:0;font-size:1em;white-space:pre}
.doc pre .copy{position:absolute;top:8px;right:8px;background:rgba(255,255,255,.06);color:var(--code-fg);border:1px solid rgba(255,255,255,.18);border-radius:6px;padding:3px 9px;font:500 .7rem/1 "Inter",sans-serif;cursor:pointer;opacity:0;transition:opacity .15s,background .15s,border-color .15s}
.doc pre:hover .copy,.doc pre .copy:focus{opacity:1}
.doc pre .copy:hover{background:rgba(255,255,255,.14)}
.doc pre .copy.copied{background:var(--accent);border-color:var(--accent);opacity:1}
.doc pre .hl-c{color:var(--hl-comment);font-style:italic}
.doc pre .hl-s{color:var(--hl-string)}
.doc pre .hl-n{color:var(--hl-number)}
.doc pre .hl-k{color:var(--hl-keyword);font-weight:600}
.doc pre .hl-f{color:var(--hl-flag)}
.doc pre .hl-m{color:var(--hl-meta);font-weight:600}
.doc pre .hl-p{color:var(--hl-prompt);user-select:none}
.doc pre .hl-cmd{color:var(--hl-keyword);font-weight:600}
.doc blockquote{margin:1.5em 0;padding:12px 18px;border-left:3px solid var(--accent);background:var(--accent-soft);border-radius:0 9px 9px 0;color:var(--text)}
.doc blockquote p:last-child{margin-bottom:0}
.doc table{width:100%;border-collapse:collapse;margin:1.3em 0;font-size:.92em}
.doc th,.doc td{border-bottom:1px solid var(--line);padding:10px 11px;text-align:left;vertical-align:top}
.doc th{font-weight:600;color:var(--ink);background:var(--line-soft);border-bottom:1px solid var(--line)}
.doc hr{border:0;border-top:1px solid var(--line);margin:2.4em 0}
.toc{position:sticky;top:32px;align-self:start;font-size:.84rem;padding-left:14px;border-left:1px solid var(--line);max-height:calc(100vh - 64px);overflow:auto;scrollbar-width:thin;scrollbar-color:var(--line) transparent}
.toc::-webkit-scrollbar{width:5px}
.toc::-webkit-scrollbar-thumb{background:var(--line);border-radius:5px}
.toc h2{font-size:.65rem;color:var(--subtle);text-transform:uppercase;letter-spacing:.1em;margin:0 0 10px;font-weight:600}
.toc a{display:block;color:var(--muted);text-decoration:none;padding:4px 0 4px 10px;line-height:1.35;border-left:2px solid transparent;margin-left:-12px;transition:color .12s,border-color .12s}
.toc a:hover{color:var(--ink);text-decoration:none}
.toc a.active{color:var(--accent);border-left-color:var(--accent);font-weight:500}
.toc-l3{padding-left:22px!important;font-size:.94em}
@media(max-width:1179px){.toc{display:none}}
.page-nav{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:56px;border-top:1px solid var(--line);padding-top:24px}
.page-nav>a{display:block;border:1px solid var(--line);background:transparent;border-radius:9px;padding:13px 16px;text-decoration:none;color:var(--text);transition:border-color .15s,transform .15s,box-shadow .15s,background-color .18s}
.page-nav>a:hover{border-color:var(--accent);text-decoration:none;color:var(--ink);background:var(--accent-soft)}
.page-nav small{display:block;color:var(--muted);font-size:.66rem;text-transform:uppercase;letter-spacing:.1em;margin-bottom:5px;font-weight:600}
.page-nav span{display:block;font-weight:600;line-height:1.3;color:var(--ink)}
.page-nav-prev{text-align:left}
.page-nav-next{text-align:right;grid-column:2}
.page-nav-prev:only-child{grid-column:1}
.nav-toggle{display:none;position:fixed;top:14px;right:14px;top:calc(14px + env(safe-area-inset-top, 0px));right:calc(14px + env(safe-area-inset-right, 0px));z-index:20;width:40px;height:40px;border-radius:9px;background:var(--paper);border:1px solid var(--line);color:var(--ink);cursor:pointer;padding:10px 9px;flex-direction:column;align-items:stretch;justify-content:space-between;box-shadow:var(--shadow-card)}
.nav-toggle span{display:block;width:100%;height:2px;flex:0 0 2px;background:currentColor;border-radius:2px;transition:transform .2s,opacity .2s}
.nav-toggle[aria-expanded="true"] span:nth-child(1){transform:translateY(8px) rotate(45deg)}
.nav-toggle[aria-expanded="true"] span:nth-child(2){opacity:0}
.nav-toggle[aria-expanded="true"] span:nth-child(3){transform:translateY(-8px) rotate(-45deg)}
@media(max-width:900px){
  .shell{display:block}
  .sidebar{position:fixed;inset:0 30% 0 0;max-width:320px;height:100vh;z-index:15;transform:translateX(-100%);transition:transform .25s ease,background-color .2s,border-color .2s;box-shadow:0 18px 40px rgba(0,0,0,.32);background:var(--bg);pointer-events:none}
  .sidebar.open{transform:translateX(0);pointer-events:auto}
  .nav-toggle{display:flex}
  main{padding:64px 18px 56px}
  .hero{padding-top:6px}
  .hero h1{font-size:1.8rem}
  .home-hero{grid-template-columns:1fr;gap:24px;padding-top:8px}
  .hero-art{min-height:180px}
  .hero-art svg{max-width:380px}
  .home-hero h1{font-size:2.4rem;max-width:none}
  .doc h1{font-size:2.05rem}
  .hero-meta{width:100%;justify-content:flex-start}
  .doc{padding:0}
  .doc-grid{margin-top:18px;gap:24px}
  .doc :is(h2,h3,h4) .anchor{display:none}
}
@media(max-width:520px){
  main{padding:60px 14px 48px}
  .doc pre{margin-left:-14px;margin-right:-14px;border-radius:0;border-left:0;border-right:0}
}
`;
}

export function js() {
  return `
const themeRoot=document.documentElement;
function applyTheme(mode){themeRoot.dataset.theme=mode;document.querySelectorAll('[data-theme-toggle]').forEach(b=>b.setAttribute('aria-pressed',mode==='dark'?'true':'false'))}
function storedTheme(){try{return localStorage.getItem('theme')}catch(e){return null}}
function persistTheme(mode){try{localStorage.setItem('theme',mode)}catch(e){}}
applyTheme(themeRoot.dataset.theme==='light'?'light':'dark');
document.querySelectorAll('[data-theme-toggle]').forEach(btn=>{btn.addEventListener('click',()=>{const next=themeRoot.dataset.theme==='dark'?'light':'dark';applyTheme(next);persistTheme(next)})});
const sidebar=document.querySelector('.sidebar');
const toggle=document.querySelector('.nav-toggle');
const mobileNav=window.matchMedia('(max-width: 900px)');
const sidebarFocusable='a[href],button,input,select,textarea,[tabindex]';
function setSidebarFocusable(enabled){
  sidebar?.querySelectorAll(sidebarFocusable).forEach((el)=>{
    if(enabled){
      if(el.dataset.sidebarTabindex!==undefined){
        if(el.dataset.sidebarTabindex)el.setAttribute('tabindex',el.dataset.sidebarTabindex);
        else el.removeAttribute('tabindex');
        delete el.dataset.sidebarTabindex;
      }
    }else if(el.dataset.sidebarTabindex===undefined){
      el.dataset.sidebarTabindex=el.getAttribute('tabindex')??'';
      el.setAttribute('tabindex','-1');
    }
  });
}
function setSidebarOpen(open){
  if(!sidebar||!toggle)return;
  sidebar.classList.toggle('open',open);
  toggle.setAttribute('aria-expanded',open?'true':'false');
  if(mobileNav.matches){
    sidebar.inert=!open;
    if(open)sidebar.removeAttribute('aria-hidden');
    else sidebar.setAttribute('aria-hidden','true');
    setSidebarFocusable(open);
  }else{
    sidebar.inert=false;
    sidebar.removeAttribute('aria-hidden');
    setSidebarFocusable(true);
  }
}
setSidebarOpen(false);
toggle?.addEventListener('click',()=>setSidebarOpen(!sidebar?.classList.contains('open')));
document.addEventListener('click',(e)=>{if(!sidebar?.classList.contains('open'))return;if(sidebar.contains(e.target)||toggle?.contains(e.target))return;setSidebarOpen(false)});
document.addEventListener('keydown',(e)=>{if(e.key==='Escape')setSidebarOpen(false)});
const syncSidebarForViewport=()=>setSidebarOpen(sidebar?.classList.contains('open')??false);
if(mobileNav.addEventListener)mobileNav.addEventListener('change',syncSidebarForViewport);
else mobileNav.addListener?.(syncSidebarForViewport);
const input=document.getElementById('doc-search');
input?.addEventListener('input',()=>{const q=input.value.trim().toLowerCase();document.querySelectorAll('nav section').forEach(sec=>{let any=false;sec.querySelectorAll('.nav-link').forEach(a=>{const m=!q||a.textContent.toLowerCase().includes(q);a.style.display=m?'block':'none';if(m)any=true});sec.style.display=any?'block':'none'})});
function attachCopy(target,getText){const btn=document.createElement('button');btn.type='button';btn.className='copy';btn.textContent='Copy';btn.addEventListener('click',async()=>{try{await navigator.clipboard.writeText(getText());btn.textContent='Copied';btn.classList.add('copied');setTimeout(()=>{btn.textContent='Copy';btn.classList.remove('copied')},1400)}catch{btn.textContent='Failed';setTimeout(()=>{btn.textContent='Copy'},1400)}});target.appendChild(btn)}
document.querySelectorAll('.doc pre').forEach(pre=>attachCopy(pre,()=>pre.querySelector('code')?.textContent??''));
const tocLinks=document.querySelectorAll('.toc a');
if(tocLinks.length){const map=new Map();tocLinks.forEach(a=>{const id=a.getAttribute('href').slice(1);const el=document.getElementById(id);if(el)map.set(el,a)});const setActive=l=>{tocLinks.forEach(x=>x.classList.remove('active'));l.classList.add('active')};const obs=new IntersectionObserver(entries=>{const visible=entries.filter(e=>e.isIntersecting).sort((a,b)=>a.boundingClientRect.top-b.boundingClientRect.top);if(visible.length){const link=map.get(visible[0].target);if(link)setActive(link)}},{rootMargin:'-15% 0px -65% 0px',threshold:0});map.forEach((_,el)=>obs.observe(el))}
`;
}

export function preThemeScript() {
  return `(function(){var s;try{s=localStorage.getItem('theme')}catch(e){}document.documentElement.dataset.theme=s==='light'?'light':'dark'})();`;
}

export function themeToggleHtml() {
  return `<button class="theme-toggle" type="button" aria-label="Toggle dark mode" aria-pressed="true" data-theme-toggle>
    <svg class="theme-icon-moon" viewBox="0 0 20 20" aria-hidden="true"><path d="M14.6 12.1A6.5 6.5 0 0 1 7.4 2.7a6.5 6.5 0 1 0 7.2 9.4z" fill="currentColor"/></svg>
    <svg class="theme-icon-sun" viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="3.4" fill="currentColor"/><g stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><line x1="10" y1="2" x2="10" y2="4"/><line x1="10" y1="16" x2="10" y2="18"/><line x1="2" y1="10" x2="4" y2="10"/><line x1="16" y1="10" x2="18" y2="10"/><line x1="4.2" y1="4.2" x2="5.6" y2="5.6"/><line x1="14.4" y1="14.4" x2="15.8" y2="15.8"/><line x1="4.2" y1="15.8" x2="5.6" y2="14.4"/><line x1="14.4" y1="5.6" x2="15.8" y2="4.2"/></g></svg>
  </button>`;
}

export function brandMarkSvg() {
  return `<svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M2 13h4l2-7 3 13 3-9 2 5h6" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>`;
}

export function ekgArtSvg() {
  const path = "M 14 115 L 36 115 L 42 103 L 48 115 L 66 115 L 72 125 L 77 45 L 82 150 L 87 115 L 109 115 L 119 98 L 129 115 L 156 115 L 162 103 L 168 115 L 186 115 L 192 125 L 197 45 L 202 150 L 207 115 L 229 115 L 239 98 L 249 115 L 276 115 L 282 103 L 288 115 L 306 115 L 312 125 L 317 45 L 322 150 L 327 115 L 346 115";
  return `<svg viewBox="0 0 360 200" xmlns="http://www.w3.org/2000/svg" aria-hidden="true" focusable="false">
<defs>
<linearGradient id="ekg-stroke" x1="0" y1="0" x2="1" y2="0"><stop offset="0%" stop-color="#4ade80"/><stop offset="100%" stop-color="#14b8a6"/></linearGradient>
<linearGradient id="ekg-card" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#0f172a"/><stop offset="100%" stop-color="#060a14"/></linearGradient>
<filter id="ekg-glow" x="-30%" y="-30%" width="160%" height="160%"><feGaussianBlur stdDeviation="2.4" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
</defs>
<rect x="0" y="0" width="360" height="200" rx="16" fill="url(#ekg-card)" stroke="#1e293b" stroke-width="1"/>
<g transform="translate(18 20)"><circle cx="0" cy="0" r="4.5" fill="#ef5350" opacity=".85"/><circle cx="13" cy="0" r="4.5" fill="#fbbf24" opacity=".85"/><circle cx="26" cy="0" r="4.5" fill="#4ade80" opacity=".85"/></g>
<text x="344" y="24" font-family="JetBrains Mono, ui-monospace, monospace" font-size="9" letter-spacing="1.5" fill="#64748b" text-anchor="end">EKG · LIVE</text>
<g stroke="#1e293b" stroke-width=".7" opacity=".55"><line x1="14" y1="60" x2="346" y2="60"/><line x1="14" y1="115" x2="346" y2="115"/><line x1="14" y1="170" x2="346" y2="170"/></g>
<g stroke="#1e293b" stroke-width=".5" opacity=".32"><line x1="66" y1="48" x2="66" y2="178"/><line x1="129" y1="48" x2="129" y2="178"/><line x1="192" y1="48" x2="192" y2="178"/><line x1="249" y1="48" x2="249" y2="178"/><line x1="312" y1="48" x2="312" y2="178"/></g>
<path d="${path}" stroke="url(#ekg-stroke)" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round" opacity=".28"/>
<path class="ekg-sweep" d="${path}" stroke="url(#ekg-stroke)" stroke-width="2.2" fill="none" stroke-linecap="round" stroke-linejoin="round" filter="url(#ekg-glow)"/>
<g transform="translate(18 184)" font-family="JetBrains Mono, ui-monospace, monospace" font-size="9" letter-spacing="1" fill="#64748b"><circle class="ekg-dot" cx="0" cy="-3" r="2.4" fill="#4ade80"/><text x="10" y="0">72 BPM</text></g>
<text x="344" y="184" font-family="JetBrains Mono, ui-monospace, monospace" font-size="9" letter-spacing="1" fill="#64748b" text-anchor="end">ARCHIVE READY</text>
</svg>`;
}

const ICON_PATHS = {
  directions_walk: "m280-40 112-564-72 28v136h-80v-188l202-86q14-6 29.5-7t29.5 4q14 5 26.5 14t20.5 23l40 64q26 42 70.5 69T760-520v80q-70 0-125-29t-94-74l-25 123 84 80v300h-80v-260l-84-64-72 324h-84Zm203.5-723.5Q460-787 460-820t23.5-56.5Q507-900 540-900t56.5 23.5Q620-853 620-820t-23.5 56.5Q573-740 540-740t-56.5-23.5Z",
  favorite: "m480-120-58-52q-101-91-167-157T150-447.5Q111-500 95.5-544T80-634q0-94 63-157t157-63q52 0 99 22t81 62q34-40 81-62t99-22q94 0 157 63t63 157q0 46-15.5 90T810-447.5Q771-395 705-329T538-172l-58 52Zm0-108q96-86 158-147.5t98-107q36-45.5 50-81t14-70.5q0-60-40-100t-100-40q-47 0-87 26.5T518-680h-76q-15-41-55-67.5T300-774q-60 0-100 40t-40 100q0 35 14 70.5t50 81q36 45.5 98 107T480-228Zm0-273Z",
  monitor_heart: "M80-600v-120q0-33 23.5-56.5T160-800h640q33 0 56.5 23.5T880-720v120h-80v-120H160v120H80Zm80 440q-33 0-56.5-23.5T80-240v-120h80v120h640v-120h80v120q0 33-23.5 56.5T800-160H160Zm261-125.5q10-5.5 15-16.5l124-248 44 88q5 11 15 16.5t21 5.5h240v-80H665l-69-138q-5-11-15-15.5t-21-4.5q-11 0-21 4.5T524-658L400-410l-44-88q-5-11-15-16.5t-21-5.5H80v80h215l69 138q5 11 15 16.5t21 5.5q11 0 21-5.5ZM480-480Z",
  bedtime: "M484-80q-84 0-157.5-32t-128-86.5Q144-253 112-326.5T80-484q0-146 93-257.5T410-880q-18 99 11 193.5T521-521q71 71 165.5 100T880-410q-26 144-138 237T484-80Zm0-80q88 0 163-44t118-121q-86-8-163-43.5T464-465q-61-61-97-138t-43-163q-77 43-120.5 118.5T160-484q0 135 94.5 229.5T484-160Zm-20-305Z",
  fitness_center: "m536-84-56-56 142-142-340-340-142 142-56-56 56-58-56-56 84-84-56-58 56-56 58 56 84-84 56 56 58-56 56 56-142 142 340 340 142-142 56 56-56 58 56 56-84 84 56 58-56 56-58-56-84 84-56-56-58 56Z",
  monitor_weight: "M480-480q50 0 85-35t35-85q0-50-35-85t-85-35q-50 0-85 35t-35 85q0 50 35 85t85 35Zm-74-106q-6-6-6-14t6-14q6-6 14-6t14 6q6 6 6 14t-6 14q-6 6-14 6t-14-6Zm60 0q-6-6-6-14t6-14q6-6 14-6t14 6q6 6 6 14t-6 14q-6 6-14 6t-14-6Zm60 0q-6-6-6-14t6-14q6-6 14-6t14 6q6 6 6 14t-6 14q-6 6-14 6t-14-6ZM200-120q-33 0-56.5-23.5T120-200v-560q0-33 23.5-56.5T200-840h560q33 0 56.5 23.5T840-760v560q0 33-23.5 56.5T760-120H200Zm0-80h560v-560H200v560Zm0-560v560-560Z",
  bloodtype: "M251.5-174Q160-268 160-408q0-100 79.5-217.5T480-880q161 137 240.5 254.5T800-408q0 140-91.5 234T480-80q-137 0-228.5-94ZM652-230.5Q720-301 720-408q0-73-60.5-165T480-774Q361-665 300.5-573T240-408q0 107 68 177.5T480-160q104 0 172-70.5ZM360-240h240v-80H360v80Zm80-120h80v-80h80v-80h-80v-80h-80v80h-80v80h80v80Zm40-120Z",
  route: "M247-167q-47-47-47-113v-327q-35-13-57.5-43.5T120-720q0-50 35-85t85-35q50 0 85 35t35 85q0 39-22.5 69.5T280-607v327q0 33 23.5 56.5T360-200q33 0 56.5-23.5T440-280v-400q0-66 47-113t113-47q66 0 113 47t47 113v327q35 13 57.5 43.5T840-240q0 50-35 85t-85 35q-50 0-85-35t-35-85q0-39 22.5-70t57.5-43v-327q0-33-23.5-56.5T600-760q-33 0-56.5 23.5T520-680v400q0 66-47 113t-113 47q-66 0-113-47Zm-7-513q17 0 28.5-11.5T280-720q0-17-11.5-28.5T240-760q-17 0-28.5 11.5T200-720q0 17 11.5 28.5T240-680Zm480 480q17 0 28.5-11.5T760-240q0-17-11.5-28.5T720-280q-17 0-28.5 11.5T680-240q0 17 11.5 28.5T720-200ZM240-720Zm480 480Z",
  watch: "M420-800h120-120Zm0 640h120-120Zm-60 80-54-182q-48-38-77-95t-29-123q0-66 29-123t77-95l54-182h240l54 182q48 38 77 95t29 123q0 66-29 123t-77 95L600-80H360Zm261.5-258.5Q680-397 680-480t-58.5-141.5Q563-680 480-680t-141.5 58.5Q280-563 280-480t58.5 141.5Q397-280 480-280t141.5-58.5ZM404-750q20-5 38.5-8t37.5-3q19 0 37.5 3t38.5 8l-16-50H420l-16 50Zm16 590h120l16-50q-20 5-38.5 7.5T480-200q-19 0-37.5-2.5T404-210l16 50Z",
  bar_chart: "M640-160v-280h160v280H640Zm-240 0v-640h160v640H400Zm-240 0v-440h160v440H160Z",
  ecg_heart: "M480-480Zm0 360q-18 0-34.5-6.5T416-146L148-415q-35-35-51.5-80T80-589q0-103 67-177t167-74q48 0 90.5 19t75.5 53q32-34 74.5-53t90.5-19q100 0 167.5 74T880-590q0 49-17 94t-51 80L543-146q-13 13-29 19.5t-34 6.5Zm40-520q10 0 19 5t14 13l68 102h166q7-17 10.5-34.5T801-590q-2-69-46-118.5T645-758q-31 0-59.5 12T536-711l-27 29q-5 6-13 9.5t-16 3.5q-8 0-16-3.5t-14-9.5l-27-29q-21-23-49-36t-60-13q-66 0-110 50.5T160-590q0 18 3 35.5t10 34.5h187q10 0 19 5t14 13l35 52 54-162q4-12 14.5-20t23.5-8Zm12 130-54 162q-4 12-15 20t-24 8q-10 0-19-5t-14-13l-68-102H236l237 237q2 2 3.5 2.5t3.5.5q2 0 3.5-.5t3.5-2.5l236-237H600q-10 0-19-5t-15-13l-34-52Z",
  vital_signs: "M326-171q-15-11-22-28l-92-241H80q-17 0-28.5-11.5T40-480q0-17 11.5-28.5T80-520h160q13 0 22.5 7t14.5 19l83 218 184-485q7-17 22-28t34-11q19 0 34 11t22 28l92 241h132q17 0 28.5 11.5T920-480q0 17-11.5 28.5T880-440H720q-13 0-22.5-7T683-466l-83-218-184 485q-7 17-22 28t-34 11q-19 0-34-11Z",
  scale: "M280-80H122q-17 0-28.5-12.5T83-122q9-151 86-259.5T400-520v-120q-39-5-88-19t-94.5-38Q172-721 137-754.5T92-831q-5-19 6.5-34t31.5-15h700q20 0 31.5 15t6.5 34q-10 43-45 76.5T742.5-697Q697-673 648-659t-88 19v120q154 30 231 138.5T877-122q1 17-10.5 29.5T838-80H680q-17 0-28.5-11.5T640-120q0-17 11.5-28.5T680-160h115q-18-152-113.5-220T480-448q-106 0-201.5 68T165-160h115q17 0 28.5 11.5T320-120q0 17-11.5 28.5T280-80Zm362-659.5Q713-764 755-800H205q42 36 113 60.5T480-715q91 0 162-24.5ZM480-80q-33 0-56.5-23.5T400-160q0-17 6.5-31t17.5-25q12-12 32.5-24.5T505-266l92-37q12-5 21.5 4.5T623-277l-37 92q-13 28-25.5 48.5T536-104q-11 11-25 17.5T480-80Zm0-635Z",
  glucose: "M576-80q-35 0-67-14.5T454-136L250-374q-11-14-9.5-30.5T256-432l18-13q20-15 45-16t46 13l55 35v-387q0-17 11.5-28.5T460-840q17 0 28.5 11.5T500-800v460q0 24-21 35t-41-2l-56-36 144 169q6 7 14 10.5t17 3.5h203q33 0 56.5-23.5T840-240v-280q0-17 11.5-28.5T880-560q17 0 28.5 11.5T920-520v280q0 66-47 113t-113 47H576Zm24-600q17 0 28.5 11.5T640-640v160q0 17-11.5 28.5T600-440q-17 0-28.5-11.5T560-480v-160q0-17 11.5-28.5T600-680Zm140 40q17 0 28.5 11.5T780-600v120q0 17-11.5 28.5T740-440q-17 0-28.5-11.5T700-480v-120q0-17 11.5-28.5T740-640Zm-560 80q-59 0-99.5-40.5T40-698q0-34 13.5-59t63.5-82l33-37q12-14 30-14t30 14l33 38q51 59 64 83t13 57q0 57-41 97.5T180-560Zm0-80q25 0 42.5-17t17.5-41q0-17-8.5-30.5T185-784l-5-5-5 5q-32 36-43.5 54T120-698q0 24 17 41t43 17Zm0-58Zm660 538H526h314Z",
  water_drop: "M480-80q-117 0-198.5-81.5T200-360q0-101 64.5-180.5T480-880q151 80 215.5 159.5T760-360q0 117-81.5 198.5T480-80Zm0-80q83 0 141.5-58.5T680-360q0-69-43-128.5T480-770Q366-689 323-629t-43 129q0 83 58.5 141.5T480-160Zm0-300Z",
  exercise: "m314-40 86-348-92 36v152h-80v-204l228-98q23-9 38.5-12t29.5-3q21 0 39.5 11t29.5 32l40 64q26 41 70.5 67.5T800-320v80q-70 0-128-26t-104-66l-30 144 84 80v228h-80v-167l-89-86-70 273H314Zm258-694q-33 0-56.5-23.5T492-814q0-33 23.5-56.5T572-894q33 0 56.5 23.5T652-814q0 33-23.5 56.5T572-734Z",
  air: "M580-160q-42 0-71-29t-29-71h80q0 8 6 14t14 6q8 0 14-6t6-14q0-8-6-14t-14-6H80v-80h500q42 0 71 29t29 71q0 42-29 71t-71 29Zm160-200H80v-80h660q25 0 42.5-17.5T800-500q0-25-17.5-42.5T740-560q-25 0-42.5 17.5T680-500h-80q0-58 41-99t99-41q58 0 99 41t41 99q0 58-41 99t-99 41Zm-360-200H80v-80h300q25 0 42.5-17.5T440-700q0-25-17.5-42.5T380-760q-25 0-42.5 17.5T320-700h-80q0-58 41-99t99-41q58 0 99 41t41 99q0 58-41 99t-99 41Z",
};

export function iconSvg(name) {
  const d = ICON_PATHS[name];
  if (!d) throw new Error(`iconSvg: unknown icon "${name}" — add its path to ICON_PATHS in scripts/docs-site-assets.mjs`);
  return `<svg class="cap-icon" viewBox="0 -960 960 960" xmlns="http://www.w3.org/2000/svg" aria-hidden="true" focusable="false"><path d="${d}" fill="currentColor"/></svg>`;
}

export function faviconSvg() {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" role="img" aria-label="gohealthcli">
<defs>
<linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
<stop offset="0%" stop-color="#1e293b"/>
<stop offset="100%" stop-color="#0a0f1a"/>
</linearGradient>
<linearGradient id="ekg" x1="0" y1="0" x2="1" y2="0">
<stop offset="0%" stop-color="#4ade80"/>
<stop offset="100%" stop-color="#14b8a6"/>
</linearGradient>
</defs>
<rect width="64" height="64" rx="13" fill="url(#bg)"/>
<path d="M6 33h7l4-13 6 24 5-17 4 9h26" stroke="url(#ekg)" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
</svg>`;
}
