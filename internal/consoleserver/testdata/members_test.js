const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const app = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');
function extract(start, end) {
  const from = app.indexOf(start), to = app.indexOf(end, from);
  assert.ok(from > 0 && to > from);
  vm.runInThisContext(app.slice(from, to));
}
extract('function memberName(', 'function adminResourcePath(');
extract('async function loadNamespaceOptions(', 'function defaultSessionYAML(');
extract('async function openDialog(', 'elements.newSessionButton.addEventListener(');

function element() {
  return {
    hidden: false, disabled: false, value: '', textContent: '', children: [], open: false,
    append(...children) { this.children.push(...children); },
    replaceChildren(...children) { this.children = children; },
    addEventListener() {}, setAttribute() {}, focus() {},
    showModal() { this.open = true; }, close() { this.open = false; },
  };
}
const member = {id: 'Ym9i', username: 'oidc:bob', role: 'admin', version: '3', sources: [{binding: 'binding', role: 'admin', managed: true}], canChange: true, canRemove: true};
const inventory = {enabled: true, usernamePrefix: 'oidc:', currentUser: 'oidc:alice', roles: [{name: 'user', label: 'User', description: 'Use Sessions', canAssign: true}, {name: 'admin', label: 'Admin', description: 'Manage members', canAssign: true}], members: [member], groups: [{name: 'oidc:developers', role: 'user', binding: 'developers'}]};
function reset() {
  global.state = {namespace: 'team-a', namespaceGeneration: 0, defaultNamespace: 'team-a', memberGeneration: 0, memberInventory: null, memberSaving: false, memberEditing: null};
  global.elements = Object.fromEntries(['memberStatus', 'memberList', 'memberGroups', 'memberError', 'addMember', 'memberDialog', 'memberDialogTitle', 'memberDialogDescription', 'memberSubject', 'memberRole', 'memberHelp', 'memberDialogError', 'memberSave', 'activeNamespace', 'namespace', 'namespaceStatus', 'refreshNamespaces', 'newSessionButton', 'welcomeNew', 'dialog', 'dialogError'].map(key => [key, element()]));
  elements.namespaceLabels = [element()];
  global.document = {createElement: element};
  global.addOption = (select, value, label) => { const option = element(); option.value = value; option.textContent = label; select.append(option); };
  global.errorMessage = error => error.message;
  global.showToast = () => {};
  global.renderOverview = global.renderResources = global.renderSessions = () => {};
  global.setConsoleView = global.setCreationMode = () => {};
  global.loadOptions = async () => {};
  global.adminRefreshes = 0;
  global.loadAdminResources = async () => { adminRefreshes++; };
  global.loadAdmin = async () => { await Promise.all([loadMembers(), loadAdminResources()]); };
  global.switchNamespace = async namespace => { state.namespace = namespace; state.namespaceGeneration++; };
  global.window = {confirm: () => true, setTimeout: () => {}, localStorage: new class {
    values = new Map();
    getItem(key) { return this.values.get(key) || null; }
    setItem(key, value) { this.values.set(key, value); }
    removeItem(key) { this.values.delete(key); }
  }};
}
async function testMembers() {
  reset();
  global.api = async url => {
    assert.equal(url, '/api/admin/members?namespace=team-a');
    return {...inventory, members: [member, {...member, username: 'oidc:external', sources: [{binding: 'outside', role: 'admin', managed: false}], canChange: false, canRemove: false}]};
  };
  await loadMembers();
  assert.equal(elements.addMember.hidden, false);
  assert.equal(elements.memberList.children[0].children[0].children[0].textContent, 'bob');
  assert.deepEqual(elements.memberList.children[0].children[1].children.map(child => child.textContent), ['Admin', 'Change role', 'Remove member']);
  assert.equal(elements.memberList.children[1].children[1].children.length, 1);
  assert.equal(elements.memberList.children[1].children[0].children[1].textContent, 'External access: outside (Admin)');
  assert.equal(elements.memberGroups.children[0].textContent, 'Access through groups');
  global.api = async () => ({enabled: false});
  await loadMembers();
  assert.equal(elements.addMember.hidden, true);
  assert.equal(elements.memberList.children.length, 0);
  assert.match(elements.memberStatus.textContent, /require OIDC/);
  global.api = async () => { throw new Error('access denied'); };
  await loadMembers();
  assert.equal(elements.memberStatus.textContent, 'Unable to load members: access denied');
}
async function testAddAndChange() {
  reset(); state.memberInventory = inventory;
  openMemberDialog();
  assert.equal(elements.memberDialog.open, true);
  assert.equal(elements.memberRole.value, 'user');
  assert.equal(elements.memberDialogDescription.textContent, 'Membership in team-a');
  elements.memberSubject.value = ' bob ';
  const requests = [];
  global.api = async (url, options) => { requests.push({url, options}); return inventory; };
  await saveMember();
  assert.deepEqual(requests[0], {url: '/api/admin/members?namespace=team-a', options: {method: 'POST', body: JSON.stringify({subject: 'bob', role: 'user'})}});
  assert.equal(elements.memberDialog.open, false);
  assert.equal(adminRefreshes, 1);
  openMemberDialog(member);
  assert.equal(elements.memberSubject.disabled, true);
  elements.memberRole.value = 'user';
  await saveMember();
  assert.deepEqual(requests[2], {url: '/api/admin/members/team-a/Ym9i?version=3', options: {method: 'PUT', body: JSON.stringify({role: 'user'})}});
  assert.equal(adminRefreshes, 2);
  openMemberDialog();
  elements.memberSubject.value = 'bob';
  global.api = async () => { throw new Error('member already exists'); };
  await saveMember();
  assert.equal(elements.memberDialog.open, true);
  assert.equal(elements.memberSubject.value, 'bob');
  assert.equal(elements.memberDialogError.textContent, 'member already exists');
  assert.equal(elements.memberSave.disabled, false);
}
async function testRemoveAndSelfDemotion() {
  reset(); state.memberInventory = inventory;
  const requests = [];
  global.api = async (url, options) => { requests.push({url, options}); return inventory; };
  window.confirm = () => false;
  await removeMember(member, 'team-a', element());
  assert.equal(requests.length, 0);
  window.confirm = message => { assert.match(message, /Remove bob from team-a/); return true; };
  await removeMember(member, 'team-a', element());
  assert.deepEqual(requests[0], {url: '/api/admin/members/team-a/Ym9i?version=3', options: {method: 'DELETE'}});
  openMemberDialog({...member, username: 'oidc:alice'});
  elements.memberRole.value = 'user';
  window.confirm = message => { assert.match(message, /Change your role to User in team-a/); return false; };
  await saveMember();
  assert.equal(requests.length, 2);
}
async function testStaleNamespace() {
  reset();
  let resolve;
  global.api = () => new Promise(done => { resolve = done; });
  const request = loadMembers();
  state.namespace = 'team-b'; resolve(inventory); await request;
  assert.equal(elements.memberList.children.length, 0);
  assert.equal(state.memberInventory, null);
  state.namespace = 'team-a'; state.memberInventory = inventory;
  openMemberDialog(); elements.memberSubject.value = 'bob';
  let calls = 0;
  global.api = () => { calls++; return new Promise(done => { resolve = done; }); };
  const save = saveMember();
  assert.equal(elements.memberSubject.disabled, true);
  assert.equal(elements.memberRole.disabled, true);
  state.namespace = 'team-b'; state.memberEditing = null;
  elements.memberList.replaceChildren('team-b members');
  resolve({}); await save;
  assert.equal(state.memberSaving, false);
  assert.equal(elements.memberDialog.open, true);
  assert.equal(calls, 1);
  assert.equal(adminRefreshes, 0);
  assert.deepEqual(elements.memberList.children, ['team-b members']);
}
async function testNamespacePicker() {
  reset();
  global.loadIdentity = async () => ({defaultNamespace: 'team-a'});
  global.api = async url => { assert.equal(url, '/api/namespaces'); return {namespaces: ['team-a', 'team-b']}; };
  window.localStorage.setItem('kelos-console-namespace', 'team-b');
  await loadConfig();
  assert.equal(state.namespace, 'team-b');
  assert.equal(elements.activeNamespace.value, 'team-b');
  assert.deepEqual(elements.activeNamespace.children.map(option => option.value), ['team-a', 'team-b']);
  window.localStorage.setItem('kelos-console-namespace', 'inaccessible');
  await loadConfig();
  assert.equal(state.namespace, 'team-a');
  global.api = async () => ({namespaces: ['team-c']});
  await loadConfig();
  assert.equal(state.namespace, 'team-c');
  global.api = async () => ({namespaces: []});
  await loadConfig();
  assert.equal(state.namespace, '');
  assert.equal(elements.activeNamespace.disabled, true);
  assert.equal(elements.newSessionButton.disabled, true);
  assert.match(elements.namespaceStatus.textContent, /Ask an administrator/);
  assert.equal(window.localStorage.getItem('kelos-console-namespace'), null);
}
async function testStartupNamespaceFailure() {
  reset();
  state.namespace = '';
  window.localStorage.setItem('kelos-console-namespace', 'team-c');
  global.loadIdentity = async () => ({defaultNamespace: 'team-a'});
  global.api = async () => { throw new Error('authorization service unavailable'); };
  global.configReady = loadConfig();
  await configReady;
  assert.equal(state.namespace, '');
  assert.equal(elements.namespaceStatus.hidden, false);
  assert.match(elements.namespaceStatus.textContent, /authorization service unavailable/);
  assert.equal(elements.newSessionButton.disabled, true);
  assert.equal(elements.welcomeNew.disabled, true);
  assert.equal(window.localStorage.getItem('kelos-console-namespace'), null);
  global.api = async () => ({namespaces: ['team-a']});
  await refreshNamespaces();
  assert.equal(state.namespace, 'team-a');
  assert.equal(elements.newSessionButton.disabled, false);
  assert.equal(elements.welcomeNew.disabled, false);
  await openDialog();
  assert.equal(elements.dialog.open, true);
}
async function testRoleChangeConflict() {
  reset(); state.memberInventory = inventory;
  openMemberDialog(member); elements.memberRole.value = 'user';
  const refreshed = {...inventory, members: [{...member, version: '4'}]};
  const requests = [];
  global.api = async (url, options) => {
    requests.push({url, options});
    if (options) throw new Error('membership changed');
    return refreshed;
  };
  await saveMember();
  assert.deepEqual(requests.map(request => request.url), ['/api/admin/members/team-a/Ym9i?version=3', '/api/admin/members?namespace=team-a']);
  assert.equal(adminRefreshes, 1);
  assert.equal(state.memberInventory.members[0].version, '4');
  assert.match(elements.memberDialogError.textContent, /membership changed.*Close this dialog/);
  assert.equal(elements.memberDialogError.hidden, false);
  assert.equal(elements.memberSave.disabled, true);
  await saveMember();
  assert.equal(requests.length, 2);
  openMemberDialog(state.memberInventory.members[0]); elements.memberRole.value = 'user';
  global.api = async (url, options) => { requests.push({url, options}); return refreshed; };
  await saveMember();
  assert.equal(requests[2].url, '/api/admin/members/team-a/Ym9i?version=4');
}
async function testRemovalAlertRefresh() {
  reset(); state.memberInventory = inventory;
  global.api = async (_url, options) => {
    if (options) throw new Error('membership changed');
    return inventory;
  };
  await removeMember(member, 'team-a', element());
  assert.equal(elements.memberError.hidden, false);
  assert.equal(elements.memberError.textContent, 'membership changed');
  await loadAdmin();
  assert.equal(elements.memberError.hidden, true);
}
async function testStaleNamespaceDiscovery() {
  for (const fail of [false, true]) {
    reset();
    state.namespaces = ['team-a', 'team-b'];
    elements.activeNamespace.value = 'team-a';
    elements.activeNamespace.replaceChildren('original options');
    elements.namespaceStatus.textContent = 'current status';
    elements.namespaceStatus.hidden = true;
    let resolve, reject, switches = 0;
    global.api = () => new Promise((a, b) => { resolve = a; reject = b; });
    global.switchNamespace = async () => { switches++; };
    const refresh = refreshNamespaces();
    state.namespace = 'team-b'; state.namespaceGeneration++;
    elements.activeNamespace.value = 'team-b';
    if (fail) reject(new Error('stale failure'));
    else resolve({namespaces: ['team-a']});
    await refresh;
    assert.equal(switches, 0);
    assert.equal(state.namespace, 'team-b');
    assert.equal(elements.activeNamespace.value, 'team-b');
    assert.deepEqual(elements.activeNamespace.children, ['original options']);
    assert.deepEqual(state.namespaces, ['team-a', 'team-b']);
    assert.equal(elements.namespaceStatus.textContent, 'current status');
    assert.equal(elements.namespaceStatus.hidden, true);
  }
}
(async () => {
  await testMembers(); await testAddAndChange(); await testRemoveAndSelfDemotion(); await testStaleNamespace(); await testNamespacePicker(); await testStartupNamespaceFailure(); await testRoleChangeConflict(); await testRemovalAlertRefresh(); await testStaleNamespaceDiscovery();
  process.stdout.write('Member and namespace tests passed\n');
})().catch(error => { console.error(error); process.exitCode = 1; });
