'use strict';
const net=require('node:net');
function endpoint(raw){const u=new URL(raw);const h=u.hostname.replace(/^\[|\]$/g,'');const local=h==='localhost'||h==='::1'||net.isIP(h)===4&&h.startsWith('127.');if(u.username||u.password||u.search||u.hash||u.pathname!=='/'||!(u.protocol==='https:'||u.protocol==='http:'&&local))throw Error('Connection must be HTTPS or loopback HTTP, without credentials/path/query');return {url:u.origin,local};}
function route(path){if(typeof path!=='string'||!/^\/v1\/[a-zA-Z0-9_/?=&.%+-]+$/.test(path)||path.includes('..')||decodeURIComponent(path).includes('..')||path.toLowerCase().includes('%2f')||path.toLowerCase().includes('%5c'))throw Error('Invalid API route');return path;}
module.exports={endpoint,route};
