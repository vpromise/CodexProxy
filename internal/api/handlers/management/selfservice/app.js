'use strict';
(() => {
  const text = {
    zh: {
      eyebrow: '账号授权', title: '安全提交你的账号授权', intro: '完成提供方授权后，账号将交由管理员审核。',
      'consent-label': '我同意将此账号的身份信息和访问、刷新凭据交由本系统保存，并在管理员批准后用于代理调用。我有权提供此账号授权。',
      start: '开始 / 继续授权', authorize: '打开提供方授权页面', 'return-title': '完成授权后返回这里',
      'callback-hint': '提供方会跳转到 localhost 地址。若显示无法连接，复制浏览器地址栏的完整回调地址并粘贴到下方。也可粘贴提供方显示的 code#state。请保持此页面打开。',
      'callback-label': '授权后的回调地址', submit: '提交授权', footer: '你只需登录提供方账号，无需本平台的密码或管理权限。此链接仅用于一次账号授权。',
      invited: '邀请有效。请阅读并同意授权说明。', authorizing: '请在提供方页面完成授权，再提交回调地址。', processing: '正在处理授权，请稍候。',
      received: '授权资料已收到，等待管理员审核。你可以关闭此页面。', failed: '此次授权未完成，请向邀请者申请新链接。', unavailable: '邀请不可用或已过期，请联系邀请者。',
      error: '操作失败。请检查邀请有效性及完整回调地址，稍后重试。', consent: '请先同意授权说明。', checking: '正在检查邀请…',
    },
    en: {
      eyebrow: 'Account authorization', title: 'Submit your account authorization', intro: 'Your authorized account will be submitted for administrator review.',
      'consent-label': 'I authorize this system to store this account’s identity, access token and refresh token for proxy use after administrator approval. I am entitled to grant this authorization.',
      start: 'Start / resume authorization', authorize: 'Open provider authorization', 'return-title': 'Return here after authorization',
      'callback-hint': 'The provider redirects to localhost. If the browser cannot connect, copy the complete callback URL from its address bar and paste it below. You can also paste the provider’s code#state. Keep this page open.',
      'callback-label': 'Callback URL after authorization', submit: 'Submit authorization', footer: 'Sign in to your provider account. No platform password or administrator access is required. This invitation authorizes one account submission.',
      invited: 'Invitation verified. Please read and accept the authorization notice.', authorizing: 'Complete authorization with the provider, then submit the callback URL.', processing: 'Processing authorization. Please wait.',
      received: 'Authorization received for administrator review. You may close this page.', failed: 'Authorization did not complete. Ask the sender for a new invitation.', unavailable: 'Invitation unavailable or expired. Please contact the sender.',
      error: 'The operation failed. Check the invitation and complete callback URL before retrying.', consent: 'Please accept the authorization notice first.', checking: 'Checking invitation…',
    },
  };
  const element = (id) => document.getElementById(id);
  let language = navigator.language.startsWith('zh') ? 'zh' : 'en';
  let token = location.hash.slice(1);
  let currentStatus = 'checking';
  let busy = false;
  let timer;
  function render() {
    document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
    for (const [id, value] of Object.entries(text[language])) {
      if (element(id) && !['status', 'consent'].includes(id)) element(id).textContent = value;
    }
    element('language').textContent = language === 'zh' ? 'English' : '中文';
    element('status').textContent = text[language][currentStatus] || text[language].error;
    element('invitation').hidden = !['invited', 'authorizing', 'error', 'consent'].includes(currentStatus);
    element('start').disabled = busy;
    element('submit').disabled = busy;
  }
  function status(value) { currentStatus = value; render(); }
  async function api(path, body) {
    const response = await fetch('/oauth-enroll/' + path, {
      method: body === undefined ? 'GET' : 'POST', credentials: 'omit', cache: 'no-store', redirect: 'error',
      headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) throw new Error(response.status === 401 ? 'unavailable' : 'error');
    return response.json();
  }
  function finish() {
    status('received'); element('callback').value = ''; element('callback-section').hidden = true;
    clearTimeout(timer); token = ''; history.replaceState(null, '', location.pathname);
  }
  async function check() {
    if (!/^[a-f0-9]{32}\.[a-f0-9]{64}$/.test(token)) { status('unavailable'); return; }
    try {
      const result = await api('session');
      if (result.status === 'received') { finish(); return; }
      status(result.status);
      if (result.status === 'processing') timer = setTimeout(check, 5000);
    } catch (error) { status(error.message); }
  }
  element('language').onclick = () => { language = language === 'zh' ? 'en' : 'zh'; render(); };
  element('start').onclick = async () => {
    if (!element('consent').checked) { status('consent'); return; }
    busy = true; render();
    try {
      const result = await api('start', { consent: true });
      const target = new URL(result.url);
      if (target.protocol !== 'https:' || !['claude.ai', 'auth.openai.com'].includes(target.hostname)) throw new Error('error');
      element('authorize').href = target.href; element('callback-section').hidden = false; status('authorizing');
      element('authorize').focus();
    } catch (error) { status(error.message); }
    finally { busy = false; render(); }
  };
  element('callback-form').onsubmit = async (event) => {
    event.preventDefault(); busy = true; render();
    try { await api('complete', { callback: element('callback').value.trim() }); finish(); }
    catch (error) { status(error.message); }
    finally { busy = false; render(); }
  };
  render(); check();
})();
