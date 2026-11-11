'use strict';

renderAlerts();
renderSound();
render();
// Without the config the optional buttons stay hidden; loadAll below reports why the server is unreachable.
loadPageConfig().catch(() => {});
loadAll().then(connect).catch(err => {
  setConnection(errorText(err), true);
  if (err.status !== 401) connect();
});
