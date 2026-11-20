// Helper function to convert duration string to hours/minutes/seconds
function durationToHMS(durationStr) {
    const match = durationStr.match(/(\d+h)?(\d+m)?(\d+(?:\.\d+)?s)?/);
    let hours = 0, minutes = 0, seconds = 0;
    
    if (match) {
        if (match[1]) hours = parseInt(match[1]);
        if (match[2]) minutes = parseInt(match[2]);
        if (match[3]) seconds = parseInt(parseFloat(match[3]));
    }
    
    return { hours, minutes, seconds };
}

// Helper function to convert hours/minutes/seconds to duration string
function hmsToGoDuration(h, m, s) {
    h = parseInt(h) || 0;
    m = parseInt(m) || 0;
    s = parseInt(s) || 0;
    
    // Convert to total seconds, then format as Go duration string
    const totalSeconds = h * 3600 + m * 60 + s;
    if (totalSeconds === 0) return "0s";
    
    let result = "";
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const secs = totalSeconds % 60;
    
    if (hours > 0) result += `${hours}h`;
    if (minutes > 0) result += `${minutes}m`;
    if (secs > 0) result += `${secs}s`;
    
    return result || "0s";
}

// API base URL
const API_BASE = '/api';
const activeLogFilter = { scopeType: '', scopeID: '' };
let knownLogScopes = [];
let _currentVideoGroups = {};
let _currentSingletonVideos = {};
let _trackedChannelIDs = new Set();
let _pendingFilterActive = false;
let _feedVideoLookup = {};
let _channelsByID = {};
let _videosByID = {};

// IDs of collapse panels the user has expanded, so the periodic refresh can
// re-open them after re-rendering instead of snapping everything shut.
const _expandedCollapseIDs = new Set();

// Snapshot of the inputs behind the last channel/video render. The periodic refresh
// skips re-rendering when these are unchanged, so the DOM (focus, selection, hover,
// in-flight animations) is left alone when nothing actually changed.
let _lastChannelsRenderKey = null;
let _lastVideosRenderKey = null;

document.addEventListener('show.bs.collapse', (e) => {
    if (e.target.id) _expandedCollapseIDs.add(e.target.id);
});
document.addEventListener('hide.bs.collapse', (e) => {
    if (e.target.id) _expandedCollapseIDs.delete(e.target.id);
});

// Build a stable, selector-safe element ID from an arbitrary key (channel ID, uploader, etc.)
const collapseDomId = (prefix, key) => {
    const raw = String(key || '');
    let hash = 0;
    for (let i = 0; i < raw.length; i++) {
        hash = ((hash << 5) - hash + raw.charCodeAt(i)) | 0;
    }
    const safe = raw.replace(/[^A-Za-z0-9_-]/g, '_');
    return `${prefix}-${safe}-${(hash >>> 0).toString(36)}`;
};

// Re-open any previously expanded collapse panels inside a freshly rendered container
const restoreCollapseState = (container) => {
    if (!container) return;
    container.querySelectorAll('.collapse[id]').forEach((el) => {
        if (!_expandedCollapseIDs.has(el.id)) return;
        el.classList.add('show');
        container.querySelectorAll(`[data-bs-target="#${el.id}"]`).forEach((btn) => {
            btn.classList.remove('collapsed');
            btn.setAttribute('aria-expanded', 'true');
        });
    });
};

function togglePendingFilter() {
    _pendingFilterActive = !_pendingFilterActive;
    const btn = document.getElementById('filterPendingBtn');
    if (btn) {
        btn.classList.toggle('btn-outline-warning', !_pendingFilterActive);
        btn.classList.toggle('btn-warning', _pendingFilterActive);
    }
    loadChannels();
}

const _qualityOrder = { '360': 1, '480': 2, '720': 3, '1080': 4, '1440': 5, '2160': 6, 'best': 10 };

const leastQuality = (videos) => {
    let minOrder = Infinity;
    let minQuality = '';
    for (const v of videos) {
        const q = v.video_quality || '';
        if (!q) continue;
        const order = _qualityOrder[q] ?? 5;
        if (order < minOrder) {
            minOrder = order;
            minQuality = q;
        }
    }
    return minQuality;
};

const maxRetentionDays = (videos) => {
    let max = 0;
    for (const v of videos) {
        if ((v.retention_days || 0) > max) max = v.retention_days;
    }
    return max;
};

function hashColorFromScopeKey(scopeKey) {
    let hash = 0;
    for (let i = 0; i < scopeKey.length; i++) {
        hash = ((hash << 5) - hash) + scopeKey.charCodeAt(i);
        hash |= 0;
    }
    const hue = Math.abs(hash) % 360;
    return `hsl(${hue} 70% 62%)`;
}

// Toast helper
function showToast(message, isError = false) {
    const toast = document.getElementById('toast');
    const toastMessage = document.getElementById('toastMessage');
    toastMessage.textContent = message;
    toast.className = `toast align-items-center ${isError ? 'bg-danger text-white' : 'bg-success text-white'}`;
    const bsToast = new bootstrap.Toast(toast);
    bsToast.show();
}

// Format date
function formatDate(dateStr) {
    if (!dateStr || dateStr === '0001-01-01T00:00:00Z') return 'Never';
    const date = new Date(dateStr);
    return date.toLocaleString();
}

// Escape HTML for safe rendering of logs
// Put an "add" form back to exactly what the page shipped with: empty text fields, each
// select on its default option, checkboxes at their default state. form.reset() alone
// isn't enough because browsers may restore or autofill values, which then stick around.
function resetAddForm(form) {
    form.reset();
    for (const el of form.elements) {
        if (el.type === 'checkbox' || el.type === 'radio') {
            el.checked = el.defaultChecked;
        } else if (el.tagName === 'SELECT') {
            const def = Array.from(el.options).find(o => o.defaultSelected) || el.options[0];
            el.value = def ? def.value : '';
        } else if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
            el.value = el.defaultValue || '';
        }
    }
}

const ADD_FORM_IDS = ['addChannelForm', 'addVideoForm'];

// Clear the add forms on load and when the browser restores the page from its
// back/forward cache, so a previously typed URL never reappears.
function resetAllAddForms() {
    for (const id of ADD_FORM_IDS) {
        const form = document.getElementById(id);
        if (form) resetAddForm(form);
    }
}
window.addEventListener('pageshow', resetAllAddForms);

// Disable a form's submit button while its request is in flight so a slow add (the
// server may look the video up with yt-dlp first) can't be submitted twice.
function setFormBusy(form, busy) {
    const btn = form.querySelector('button[type="submit"]');
    if (btn) btn.disabled = busy;
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text == null ? '' : String(text);
    return div.innerHTML;
}

// Encode a value as a JavaScript string literal safe to place inside a double-quoted
// HTML attribute, e.g. onclick="fn(${jsArg(name)})". Handles quotes, backslashes and
// HTML-special characters in titles and names.
function jsArg(value) {
    return JSON.stringify(value == null ? '' : String(value))
        .replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

const convertToChannel = async (groupKey) => {
    const group = _currentVideoGroups[groupKey];
    if (!group) {
        showToast('Group data not found, please refresh', true);
        return;
    }
    const quality = leastQuality(group.videos);
    const retention = maxRetentionDays(group.videos);
    const channelExists = _trackedChannelIDs.has(group.uploaderID);
    const qualityDesc = quality || 'default';
    const retentionDesc = retention ? `${retention} days` : 'default';
    const prompt = channelExists
        ? `Move ${group.videos.length} video(s) from "${group.name}" to its existing channel subscription?\n\nExisting video files remain on disk.`
        : `Convert ${group.videos.length} video(s) from "${group.name}" to a channel subscription?\n\nQuality: ${qualityDesc}\nRetention: ${retentionDesc}\n\nExisting video files remain on disk.`;
    if (!confirm(prompt)) return;
    try {
        const response = await fetch(`${API_BASE}/videos/convert-to-channel`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                uploader_name: group.name,
                uploader_id: group.uploaderID,
                video_ids: group.videos.map(v => v.id),
                video_quality: quality,
                retention_days: retention,
            })
        });
        const data = await response.json();
        if (data.success) {
            showToast(channelExists ? `Moved ${group.videos.length} video(s) to channel "${group.name}"` : `Converted "${group.name}" to a channel`);
            loadChannels().then(loadVideos);
            loadStatus();
        } else {
            showToast(data.message || 'Failed to convert to channel', true);
        }
    } catch (err) {
        console.error('convertToChannel error:', err);
        showToast('Failed to convert to channel', true);
    }
};

const downloadFeedVideo = async (channelID, videoID) => {
    try {
        const response = await fetch(`${API_BASE}/channels/${channelID}/feed-videos/${videoID}/download`, {
            method: 'POST',
        });
        const data = await response.json();
        if (data.success) {
            showToast('Download started');
            loadDownloads();
        } else {
            showToast(data.message || 'Failed to start download', true);
        }
    } catch (err) {
        console.error('downloadFeedVideo error:', err);
        showToast('Failed to start download', true);
    }
};

const dismissFeedVideo = async (channelID, videoID) => {
    const info = _feedVideoLookup[`${channelID}/${videoID}`];
    const label = info ? `\n\nChannel: ${info.channelName}\nVideo: ${info.title}` : '';
    if (!confirm(`Dismiss this video?${label}\n\nIt will be treated as pruned and never downloaded or shown again.`)) return;
    try {
        const response = await fetch(`${API_BASE}/channels/${channelID}/feed-videos/${videoID}/dismiss`, {
            method: 'POST',
        });
        const data = await response.json();
        if (data.success) {
            showToast('Video dismissed');
            loadChannels();
        } else {
            showToast(data.message || 'Failed to dismiss video', true);
        }
    } catch (err) {
        console.error('dismissFeedVideo error:', err);
        showToast('Failed to dismiss video', true);
    }
};

const dismissAllFeedVideos = async (channelID, count) => {
    if (!confirm(`Dismiss all ${count} pending video(s)? They will be treated as pruned and never downloaded or shown again.`)) return;
    try {
        const response = await fetch(`${API_BASE}/channels/${channelID}/feed-videos/dismiss-all`, {
            method: 'POST',
        });
        const data = await response.json();
        if (data.success) {
            showToast(data.message || 'Videos dismissed');
            loadChannels();
        } else {
            showToast(data.message || 'Failed to dismiss videos', true);
        }
    } catch (err) {
        console.error('dismissAllFeedVideos error:', err);
        showToast('Failed to dismiss videos', true);
    }
};

const moveToChannel = async (videoID) => {
    const vid = _currentSingletonVideos[videoID];
    if (!vid) {
        showToast('Video data not found, please refresh', true);
        return;
    }
    const channelExists = _trackedChannelIDs.has(vid.uploaderID);
    const confirmMsg = channelExists
        ? `Move this video to its existing channel subscription "${vid.uploaderName}"?\n\nExisting video files remain on disk.`
        : `Create a channel subscription for "${vid.uploaderName}" from this video?\n\nQuality: ${vid.videoQuality || 'default'}\nRetention: ${vid.retentionDays ? `${vid.retentionDays} days` : 'default'}\n\nExisting video files remain on disk.`;
    if (!confirm(confirmMsg)) return;
    try {
        const response = await fetch(`${API_BASE}/videos/convert-to-channel`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                uploader_name: vid.uploaderName,
                uploader_id: vid.uploaderID,
                video_ids: [videoID],
                video_quality: vid.videoQuality,
                retention_days: vid.retentionDays,
            })
        });
        const data = await response.json();
        if (data.success) {
            showToast(channelExists ? `Moved video to channel "${vid.uploaderName}"` : `Created channel "${vid.uploaderName}"`);
            loadChannels().then(loadVideos);
            loadStatus();
        } else {
            showToast(data.message || 'Failed to move to channel', true);
        }
    } catch (err) {
        console.error('moveToChannel error:', err);
        showToast('Failed to move to channel', true);
    }
};

// Load status
async function loadStatus() {
    try {
        const response = await fetch(`${API_BASE}/status`);
        const data = await response.json();
        if (data.success) {
            const versionText = escapeHtml(data.data.yt_dlp_version || 'unknown');
            const appCommit = data.data.app_commit_short ? data.data.app_commit_short : (data.data.app_commit || 'unknown');
            document.getElementById('status').innerHTML = `
                <span class="badge bg-success">Running</span>
                <span class="ms-3"><strong>Channels:</strong> ${data.data.channels_count}</span>
                <span class="ms-3"><strong>Videos:</strong> ${data.data.videos_count}</span>
                <span class="ms-3"><strong>yt-dlp:</strong> ${versionText}</span>
                <span class="ms-3"><strong>Commit:</strong> ${escapeHtml(appCommit)}</span>
                ${data.data.uptime_seconds != null ? `<span class="ms-3" title="Started ${escapeHtml(new Date(data.data.started_at).toLocaleString())}"><strong>Uptime:</strong> ${formatUptime(data.data.uptime_seconds)}</span>` : ''}
                ${data.data.database ? `<span class="ms-3"><strong>Database:</strong> ${escapeHtml(data.data.database === 'postgres' ? 'PostgreSQL' : 'SQLite')}</span>` : ''}
                ${data.data.database_warning ? `<span class="badge bg-warning text-dark ms-3" title="${attr(data.data.database_warning)}"><i class="bi bi-exclamation-triangle me-1"></i>Database fallback</span>` : ''}
            `;
        }
    } catch (error) {
        document.getElementById('status').innerHTML = '<span class="badge bg-danger">Error</span>';
    }
}

// Load channels
async function loadChannels() {
    try {
        const response = await fetch(`${API_BASE}/channels`);
        const body = await response.text();
        const renderKey = `${_pendingFilterActive}|${body}`;
        if (renderKey === _lastChannelsRenderKey) return;
        const data = JSON.parse(body);
        _lastChannelsRenderKey = data.success ? renderKey : null;
        if (data.success) {
            const allChannels = (data.data || []).slice().sort((a, b) =>
                (a.name || '').localeCompare((b.name || ''), undefined, { sensitivity: 'base' })
            );
            _trackedChannelIDs = new Set(allChannels.map(ch => ch.id).filter(Boolean));
            _feedVideoLookup = {};
            _channelsByID = {};
            for (const ch of allChannels) {
                _channelsByID[ch.id] = ch;
                for (const fv of (ch.feed_videos || [])) {
                    _feedVideoLookup[`${ch.id}/${fv.id}`] = { channelName: ch.name || ch.id, title: fv.title || fv.id };
                }
            }
            const channels = _pendingFilterActive
                ? allChannels.filter(ch => {
                    const feedVideos = (ch.feed_videos || []).filter(fv => ch.download_shorts || !fv.is_short);
                    return feedVideos.length > 0;
                })
                : allChannels;
            if (allChannels.length === 0) {
                document.getElementById('channelsList').innerHTML = '<p class="text-muted">No channels configured</p>';
            } else if (channels.length === 0) {
                document.getElementById('channelsList').innerHTML = '<p class="text-muted">No channels with pending videos</p>';
            } else {
                document.getElementById('channelsList').innerHTML = channels.map((ch) => {
                    const cutoffText = ch.cutoff_date && ch.cutoff_date !== '0001-01-01T00:00:00Z' 
                        ? `<span class="badge bg-info ms-2">From: ${new Date(ch.cutoff_date).toLocaleDateString()}</span>` 
                        : '';
                    const qualityText = ch.video_quality 
                        ? `<span class="badge bg-primary ms-2">Quality: ${escapeHtml(ch.video_quality)}</span>`
                        : '';
                    const formatText = ch.video_format 
                        ? `<span class="badge bg-info ms-2">Format: ${escapeHtml(ch.video_format)}</span>`
                        : '';
                    const downloadedCount = (ch.downloaded_videos || []).length;
                    const feedVideos = (ch.feed_videos || [])
                        .filter(fv => ch.download_shorts || !fv.is_short)
                        .slice().sort((a, b) =>
                            new Date(b.published_at || 0) - new Date(a.published_at || 0)
                        );
                    const pendingCount = feedVideos.length;
                    const collapseId = collapseDomId('channel-collapse', ch.id);
                    const errorCollapseId = collapseDomId('channel-error', ch.id);
                    const pruningText = ch.disable_pruning
                        ? '<span class="badge bg-warning text-dark ms-2">No Prune</span>'
                        : '';
                    const hasError = ch.last_error && ch.last_error.trim().length > 0;
                    const errorBadge = hasError 
                        ? `<span class="badge bg-danger ms-2"><i class="bi bi-exclamation-circle"></i> Error</span>` 
                        : '';
                    const errorSection = hasError 
                        ? `
                            <div class="mt-2">
                                <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#${errorCollapseId}" aria-expanded="false">
                                    <i class="bi bi-chevron-down"></i> View Error Details
                                </button>
                                <div class="collapse mt-2" id="${errorCollapseId}">
                                    <div class="alert alert-danger" role="alert">
                                        <strong>Error:</strong> ${escapeHtml(ch.last_error)}<br>
                                        <small class="text-muted">At: ${formatDate(ch.last_error_time)}</small>
                                    </div>
                                </div>
                            </div>
                        ` 
                        : '';

                    const downloadedVideos = (ch.downloaded_videos || []).slice().sort((a, b) =>
                        new Date(b.download_date || 0) - new Date(a.download_date || 0)
                    );

                    const feedVideosHtml = feedVideos.length === 0 ? '' : `
                        <div class="mt-2 mb-3">
                            <div class="small text-warning fw-bold mb-1 d-flex justify-content-between align-items-center">
                                <span><i class="bi bi-hourglass-split me-1"></i>Pending Download (${feedVideos.length})</span>
                                <button class="btn btn-outline-danger btn-sm" onclick="dismissAllFeedVideos('${ch.id}', ${feedVideos.length})" title="Dismiss all pending videos: treat as pruned, never download or show again">
                                    <i class="bi bi-x-lg"></i> Dismiss All
                                </button>
                            </div>
                            ${feedVideos.map(fv => `
                                <div class="d-flex justify-content-between align-items-center border-bottom border-secondary py-2">
                                    <div style="flex-grow: 1;">
                                        <strong>${escapeHtml(fv.title || fv.id)}</strong>
                                        <div class="small text-muted">Published: ${formatDate(fv.published_at)}</div>
                                    </div>
                                    <div class="ms-2">
                                        <a href="https://www.youtube.com/watch?v=${fv.id}" target="_blank" class="btn btn-outline-primary btn-sm me-1" title="Watch on YouTube">
                                            <i class="bi bi-youtube"></i>
                                        </a>
                                        <button class="btn btn-success btn-sm me-1" onclick="downloadFeedVideo('${ch.id}', '${fv.id}')" title="Download this video now">
                                            <i class="bi bi-download"></i> Download
                                        </button>
                                        <button class="btn btn-outline-danger btn-sm" onclick="dismissFeedVideo('${ch.id}', '${fv.id}')" title="Dismiss: treat as pruned, never download or show again">
                                            <i class="bi bi-x-lg"></i> Dismiss
                                        </button>
                                    </div>
                                </div>
                            `).join('')}
                        </div>
                    `;

                    const childrenHtml = feedVideosHtml + (downloadedVideos.length === 0
                        ? '<div class="text-muted small py-2">No downloaded videos tracked for this channel yet</div>'
                        : downloadedVideos.map((video) => `
                            <div class="d-flex justify-content-between align-items-center border-bottom py-2">
                                <div style="flex-grow: 1;">
                                    <strong>${escapeHtml(video.title || video.id)}</strong>
                                    <div class="small text-muted">Downloaded: ${formatDate(video.download_date)}</div>
                                </div>
                                <div class="ms-2">
                                    <a href="https://www.youtube.com/watch?v=${video.id}" target="_blank" class="btn btn-outline-primary btn-sm me-1">
                                        <i class="bi bi-youtube"></i>
                                    </a>
                                    <button class="btn btn-sm ${video.disable_pruning ? 'btn-outline-warning' : 'btn-outline-secondary'}" onclick="setChannelVideoPruning('${ch.id}', '${video.id}', ${!video.disable_pruning})" title="${video.disable_pruning ? 'Click to re-enable pruning' : 'Click to keep this video indefinitely'}">
                                        <i class="bi ${video.disable_pruning ? 'bi-unlock' : 'bi-lock'}"></i>
                                    </button>
                                </div>
                            </div>
                        `).join(''));

                    const thumbHtml = ch.thumbnail_url
                        ? `<img src="${escapeHtml(ch.thumbnail_url)}" alt="" class="rounded-circle me-2 flex-shrink-0" style="width:32px;height:32px;object-fit:cover;vertical-align:middle;">`
                        : '';
                    return `
                        <div class="channel-row">
                            <div class="channel-row-header d-flex justify-content-between align-items-start">
                                <div style="flex-grow: 1;">
                                    <button class="btn btn-sm btn-outline-light me-2" type="button" data-bs-toggle="collapse" data-bs-target="#${collapseId}" aria-expanded="false" aria-controls="${collapseId}">
                                        <i class="bi bi-chevron-expand"></i>
                                    </button>
                                    ${thumbHtml}<strong>${escapeHtml(ch.name)}</strong>
                                    <span class="badge bg-secondary ms-2">${ch.retention_days || 'default'} days</span>
                                    <span class="badge bg-dark ms-2">${downloadedCount} downloaded</span>
                                    ${pendingCount > 0 ? `<span class="badge bg-warning text-dark ms-2" title="${pendingCount} video(s) tracked but not yet downloaded"><i class="bi bi-hourglass-split me-1"></i>${pendingCount} pending</span>` : ''}
                                    ${ch.skip_auto_download ? '<span class="badge bg-secondary ms-2" title="New videos are tracked but not downloaded automatically">Manual</span>' : ''}
                                    ${cutoffText}
                                    ${qualityText}
                                    ${formatText}
                                    ${pruningText}
                                    ${ch.download_shorts ? '<span class="badge bg-success ms-2">Shorts</span>' : ''}
                                    ${ch.disable_notifications ? '<span class="badge bg-secondary ms-2" title="Webhook notifications are off for this channel"><i class="bi bi-bell-slash me-1"></i>Muted</span>' : ''}
                                    ${errorBadge}<br>
                                    <small class="text-muted">${escapeHtml(ch.url)}</small><br>
                                    <span class="last-checked">Last checked: ${formatDate(ch.last_checked)}</span>
                                    ${errorSection}
                                </div>
                                <div class="ms-3">
                                    <button class="btn btn-outline-light btn-sm me-2" onclick="openScopedLogs('channel', ${jsArg(ch.id)}, ${jsArg(ch.name)})">
                                        <i class="bi bi-journal-text"></i> Logs
                                    </button>
                                    <button class="btn btn-warning btn-sm me-2" onclick="openEditChannelModal(${jsArg(ch.id)})">
                                        <i class="bi bi-pencil"></i> Edit
                                    </button>
                                    <button class="btn btn-danger btn-sm" onclick="removeChannel('${ch.id}')">
                                        <i class="bi bi-trash"></i> Remove
                                    </button>
                                </div>
                            </div>
                            <div id="${collapseId}" class="collapse channel-children">
                                <div class="channel-children-inner">
                                    ${childrenHtml}
                                </div>
                            </div>
                        </div>
                    `;
                }).join('');
                restoreCollapseState(document.getElementById('channelsList'));
            }
        }
    } catch (error) {
        _lastChannelsRenderKey = null;
        document.getElementById('channelsList').innerHTML = '<p class="text-danger">Failed to load channels</p>';
    }
}

function renderVideoRow(vid) {
    const hasError = vid.last_error && vid.last_error.trim().length > 0;
    const downloadedCount = (vid.downloaded_videos || []).length;
    const pruningText = vid.disable_pruning
        ? '<span class="badge bg-warning text-dark ms-2">No Prune</span>'
        : '';
    const downloadedText = downloadedCount > 0
        ? `<span class="badge bg-success ms-2"><i class="bi bi-check-circle"></i> Downloaded</span>`
        : '<span class="badge bg-secondary ms-2">Not downloaded</span>';
    const errorBadge = hasError
        ? `<span class="badge bg-danger ms-2"><i class="bi bi-exclamation-circle"></i> Error</span>`
        : '';
    const errorSection = hasError
        ? `
            <div class="mt-2">
                <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#video-error-${vid.id}" aria-expanded="false">
                    <i class="bi bi-chevron-down"></i> View Error Details
                </button>
                <div class="collapse mt-2" id="video-error-${vid.id}">
                    <div class="alert alert-danger" role="alert">
                        <strong>Error:</strong> ${escapeHtml(vid.last_error)}<br>
                        <small class="text-muted">At: ${formatDate(vid.last_error_time)}</small>
                    </div>
                </div>
            </div>
        `
        : '';
    const channelTracked = vid.uploader_id && _trackedChannelIDs.has(vid.uploader_id);
    const moveBtn = vid.uploader_id
        ? `<button class="btn btn-sm ${channelTracked ? 'btn-outline-success' : 'btn-outline-info'} me-2"
                onclick="moveToChannel('${vid.id}')"
                title="${channelTracked ? 'Move this video under its existing channel subscription' : 'Create a channel subscription for this uploader'}">
                <i class="bi bi-collection-play-fill"></i> ${channelTracked ? 'Move to Channel' : 'Convert to Channel'}
            </button>`
        : '';
    return `
        <div class="d-flex justify-content-between align-items-start border-bottom py-3">
            <div style="flex-grow: 1;">
                <strong>${escapeHtml(vid.title)}</strong>
                <span class="badge bg-secondary ms-2">${vid.retention_days || 'default'} days</span>
                ${vid.video_quality ? `<span class="badge bg-primary ms-2">Quality: ${escapeHtml(vid.video_quality)}</span>` : ''}
                ${vid.video_format ? `<span class="badge bg-info ms-2">Format: ${escapeHtml(vid.video_format)}</span>` : ''}
                ${pruningText}
                ${downloadedText}
                ${errorBadge}<br>
                <small class="text-muted">${escapeHtml(vid.url)}</small><br>
                <span class="last-checked">Last checked: ${formatDate(vid.last_checked)}</span>
                ${errorSection}
            </div>
            <div class="ms-3">
                <button class="btn btn-outline-light btn-sm me-2" onclick="openScopedLogs('video', ${jsArg(vid.id)}, ${jsArg(vid.title || vid.id)})">
                    <i class="bi bi-journal-text"></i> Logs
                </button>
                ${moveBtn}
                <button class="btn btn-warning btn-sm me-2" onclick="openEditVideoModal(${jsArg(vid.id)})">
                    <i class="bi bi-pencil"></i> Edit
                </button>
                <button class="btn btn-danger btn-sm" onclick="removeVideo('${vid.id}')">
                    <i class="bi bi-trash"></i> Remove
                </button>
            </div>
        </div>
    `;
}

const renderVideoGroup = (item, collapseId) => {
    const { name: groupName, videos, uploaderID, groupKey } = item;
    const sorted = videos.slice().sort((a, b) => new Date(b.added_date || 0) - new Date(a.added_date || 0));
    const newestDate = sorted[0].added_date;
    const childrenHtml = sorted.map(vid => {
        const hasError = vid.last_error && vid.last_error.trim().length > 0;
        const downloadedCount = (vid.downloaded_videos || []).length;
        const pruningText = vid.disable_pruning
            ? '<span class="badge bg-warning text-dark ms-2">No Prune</span>'
            : '';
        const downloadedText = downloadedCount > 0
            ? `<span class="badge bg-success ms-2"><i class="bi bi-check-circle"></i> Downloaded</span>`
            : '<span class="badge bg-secondary ms-2">Not downloaded</span>';
        const errorBadge = hasError
            ? `<span class="badge bg-danger ms-2"><i class="bi bi-exclamation-circle"></i> Error</span>`
            : '';
        const errorSection = hasError
            ? `
                <div class="mt-2">
                    <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#video-error-${vid.id}" aria-expanded="false">
                        <i class="bi bi-chevron-down"></i> View Error Details
                    </button>
                    <div class="collapse mt-2" id="video-error-${vid.id}">
                        <div class="alert alert-danger" role="alert">
                            <strong>Error:</strong> ${escapeHtml(vid.last_error)}<br>
                            <small class="text-muted">At: ${formatDate(vid.last_error_time)}</small>
                        </div>
                    </div>
                </div>
            `
            : '';
        return `
            <div class="d-flex justify-content-between align-items-start border-bottom py-2">
                <div style="flex-grow: 1;">
                    <strong>${escapeHtml(vid.title)}</strong>
                    <span class="badge bg-secondary ms-2">${vid.retention_days || 'default'} days</span>
                    ${vid.video_quality ? `<span class="badge bg-primary ms-2">Quality: ${escapeHtml(vid.video_quality)}</span>` : ''}
                    ${vid.video_format ? `<span class="badge bg-info ms-2">Format: ${escapeHtml(vid.video_format)}</span>` : ''}
                    ${pruningText}
                    ${downloadedText}
                    ${errorBadge}<br>
                    <small class="text-muted">${escapeHtml(vid.url)}</small><br>
                    <span class="last-checked">Added: ${formatDate(vid.added_date)} · Last checked: ${formatDate(vid.last_checked)}</span>
                    ${errorSection}
                </div>
                <div class="ms-3">
                    <button class="btn btn-outline-light btn-sm me-2" onclick="openScopedLogs('video', ${jsArg(vid.id)}, ${jsArg(vid.title || vid.id)})">
                        <i class="bi bi-journal-text"></i> Logs
                    </button>
                    <button class="btn btn-warning btn-sm me-2" onclick="openEditVideoModal(${jsArg(vid.id)})">
                        <i class="bi bi-pencil"></i> Edit
                    </button>
                    <button class="btn btn-danger btn-sm" onclick="removeVideo('${vid.id}')">
                        <i class="bi bi-trash"></i> Remove
                    </button>
                </div>
            </div>
        `;
    }).join('');

    const safeGroupKey = (groupKey || '').replace(/\\/g, '\\\\').replace(/'/g, "\\'");
    const channelExists = uploaderID && _trackedChannelIDs.has(uploaderID);
    const convertBtn = uploaderID
        ? `<button class="btn btn-sm btn-outline-success ms-2" onclick="convertToChannel('${safeGroupKey}')"><i class="bi bi-collection-play-fill"></i> ${channelExists ? 'Move to Channel' : 'Convert to Channel'}</button>`
        : '';
    const totalDownloaded = videos.reduce((sum, v) => sum + (v.downloaded_videos || []).length, 0);
    const groupDownloadedBadge = totalDownloaded > 0
        ? `<span class="badge bg-success ms-2"><i class="bi bi-check-circle"></i> Downloaded (${totalDownloaded})</span>`
        : '';

    return `
        <div class="channel-row">
            <div class="channel-row-header d-flex justify-content-between align-items-center">
                <div style="flex-grow: 1;">
                    <button class="btn btn-sm btn-outline-light me-2" type="button" data-bs-toggle="collapse" data-bs-target="#${collapseId}" aria-expanded="false" aria-controls="${collapseId}">
                        <i class="bi bi-chevron-expand"></i>
                    </button>
                    <strong>${escapeHtml(groupName)}</strong>
                    <span class="badge bg-dark ms-2">${videos.length} videos</span>
                    <span class="badge bg-secondary ms-2">Latest added: ${formatDate(newestDate)}</span>
                    ${groupDownloadedBadge}
                    ${convertBtn}
                </div>
            </div>
            <div id="${collapseId}" class="collapse channel-children">
                <div class="channel-children-inner">
                    ${childrenHtml}
                </div>
            </div>
        </div>
    `;
};

// Load videos
async function loadVideos() {
    try {
        const response = await fetch(`${API_BASE}/videos`);
        const body = await response.text();
        // Tracked channel IDs affect the Move/Convert buttons, so they're part of the key
        const renderKey = `${[..._trackedChannelIDs].sort().join(',')}|${body}`;
        if (renderKey === _lastVideosRenderKey) return;
        const data = JSON.parse(body);
        _lastVideosRenderKey = data.success ? renderKey : null;
        if (data.success) {
            const videos = data.data || [];
            _videosByID = Object.fromEntries(videos.map((v) => [v.id, v]));
            if (videos.length === 0) {
                document.getElementById('videosList').innerHTML = '<p class="text-muted">No videos configured</p>';
            } else {
                // Group by uploader_id (name as fallback); ungroupable videos stay as singletons
                const groupMap = new Map();
                const singletons = [];

                for (const vid of videos) {
                    const key = vid.uploader_id || vid.uploader || null;
                    if (key) {
                        if (!groupMap.has(key)) {
                            groupMap.set(key, { name: vid.uploader || vid.uploader_id, uploaderID: vid.uploader_id || '', videos: [] });
                        }
                        groupMap.get(key).videos.push(vid);
                    } else {
                        singletons.push(vid);
                    }
                }

                // Build top-level items: groups with 2+ videos become collapse panels; lone entries become singletons
                _currentVideoGroups = {};
                _currentSingletonVideos = {};
                const items = [];

                for (const group of groupMap.values()) {
                    if (group.videos.length >= 2) {
                        const maxDate = group.videos.reduce((max, v) => {
                            const d = new Date(v.added_date || 0);
                            return d > max ? d : max;
                        }, new Date(0));
                        const groupKey = group.uploaderID || group.name;
                        _currentVideoGroups[groupKey] = group;
                        items.push({ type: 'group', groupKey, name: group.name, uploaderID: group.uploaderID, videos: group.videos, sortDate: maxDate });
                    } else {
                        singletons.push(group.videos[0]);
                    }
                }

                for (const vid of singletons) {
                    items.push({ type: 'single', vid, sortDate: new Date(vid.added_date || 0) });
                    if (vid.uploader_id) {
                        _currentSingletonVideos[vid.id] = {
                            uploaderID: vid.uploader_id,
                            uploaderName: vid.uploader || vid.uploader_id,
                            videoQuality: vid.video_quality || '',
                            retentionDays: vid.retention_days || 0,
                        };
                    }
                }

                // Sort newest added_date first
                items.sort((a, b) => b.sortDate - a.sortDate);

                document.getElementById('videosList').innerHTML = items.map((item) => {
                    if (item.type === 'group') {
                        return renderVideoGroup(item, collapseDomId('video-group', item.groupKey));
                    }
                    return renderVideoRow(item.vid);
                }).join('');
                restoreCollapseState(document.getElementById('videosList'));
            }
        }
    } catch (error) {
        _lastVideosRenderKey = null;
        document.getElementById('videosList').innerHTML = '<p class="text-danger">Failed to load videos</p>';
    }
}

// Load config
async function loadConfig() {
    try {
        const response = await fetch(`${API_BASE}/config`);
        const data = await response.json();
        if (data.success) {
            const config = data.data;
            
            // Load check interval
            const checkInterval = durationToHMS(config.check_interval_seconds);
            document.getElementById('checkIntervalH').value = checkInterval.hours;
            document.getElementById('checkIntervalM').value = checkInterval.minutes;
            document.getElementById('checkIntervalS').value = checkInterval.seconds;
            
            document.getElementById('retentionDays').value = config.retention_days;
            document.getElementById('disablePruning').checked = !!config.disable_pruning;
            document.getElementById('downloadDir').value = config.download_dir;
            document.getElementById('mediaServerDownloadDir').value = config.media_server_download_dir || '';
            document.getElementById('fileNamePattern').value = config.file_name_pattern;
            document.getElementById('maxConcurrent').value = config.max_concurrent_downloads;
            document.getElementById('maxLogEntries').value = config.max_log_entries || 100;
            const subs = config.subtitles || {};
            document.getElementById('subtitlesEnabled').checked = !!subs.enabled;
            document.getElementById('subtitleLanguages').value = subs.languages || 'en';
            document.getElementById('subtitlesAutoGenerated').checked = !!subs.auto_generated;
            document.getElementById('defaultVideoFormat').value = config.default_video_format || 'mp4';
            document.getElementById('defaultVideoQuality').value = config.default_video_quality || '';
            
            const ytDlp = config.yt_dlp || {};
            
            // Load update interval
            const updateInterval = durationToHMS(ytDlp.update_interval_seconds || "24h0m0s");
            document.getElementById('ytDlpUpdateIntervalH').value = updateInterval.hours;
            document.getElementById('ytDlpUpdateIntervalM').value = updateInterval.minutes;
            document.getElementById('ytDlpUpdateIntervalS').value = updateInterval.seconds;
            
            // Load extractor sleep interval
            const sleepInterval = durationToHMS(ytDlp.extractor_sleep_interval_seconds || "0s");
            document.getElementById('extractorSleepIntervalH').value = sleepInterval.hours;
            document.getElementById('extractorSleepIntervalM').value = sleepInterval.minutes;
            document.getElementById('extractorSleepIntervalS').value = sleepInterval.seconds;
            
            document.getElementById('downloadThroughputLimit').value = ytDlp.download_throughput_limit || '';
            document.getElementById('ytDlpCacheDir').value = ytDlp.cache_dir || '';
            document.getElementById('restrictFilenames').checked = !!ytDlp.restrict_filenames;
            document.getElementById('cookiesBrowser').value = ytDlp.cookies_browser || '';

            loadNotificationSettings(config.notifications || {}, config.notification_events || []);
        }
    } catch (error) {
        showToast('Failed to load configuration', true);
    }
}

// Load recent logs
async function loadLogs() {
    try {
        const params = new URLSearchParams();
        if (activeLogFilter.scopeType) {
            params.set('scope_type', activeLogFilter.scopeType);
        }
        if (activeLogFilter.scopeID) {
            params.set('scope_id', activeLogFilter.scopeID);
        }
        const query = params.toString();
        const response = await fetch(`${API_BASE}/logs${query ? `?${query}` : ''}`);
        const data = await response.json();
        if (!data.success) {
            document.getElementById('logsList').innerHTML = '<span class="text-danger">Failed to load logs</span>';
            return;
        }

        const payload = data.data || {};
        const entries = payload.structured_entries || [];
        knownLogScopes = payload.scopes || knownLogScopes;
        updateLogScopeOptions();

        if (entries.length === 0) {
            document.getElementById('logsList').innerHTML = '<span class="text-muted">No logs available yet</span>';
            return;
        }

        document.getElementById('logsList').innerHTML = entries.map((entry) => {
            const scopeType = entry.scope_type || '';
            const scopeID = entry.scope_id || '';
            const scopeName = entry.scope_name || scopeID;
            const key = scopeType && scopeID ? `${scopeType}:${scopeID}` : 'global';
            const scopeColor = hashColorFromScopeKey(key);
            const label = scopeType && scopeID ? `${scopeType}:${scopeName}` : 'general';
            const pillAttrs = scopeType && scopeID
                ? `onclick="openScopedLogs('${scopeType.replace(/'/g, "\\'")}', '${scopeID.replace(/'/g, "\\'")}', '${scopeName.replace(/'/g, "\\'")}')" title="Filter to this log stream"`
                : '';
            return `<div class="log-line" style="border-left-color:${scopeColor};">
                <span class="scope-pill" style="background:${scopeColor};" ${pillAttrs}>${escapeHtml(label)}</span>${escapeHtml(entry.line || '')}
            </div>`;
        }).join('');

        const logsContainer = document.getElementById('logsList');
        logsContainer.scrollTop = logsContainer.scrollHeight;
    } catch (error) {
        document.getElementById('logsList').innerHTML = '<span class="text-danger">Failed to load logs</span>';
    }
}

function updateLogScopeOptions() {
    const typeSelect = document.getElementById('logsScopeType');
    const idSelect = document.getElementById('logsScopeId');
    if (!typeSelect || !idSelect) {
        return;
    }

    if (activeLogFilter.scopeType) {
        typeSelect.value = activeLogFilter.scopeType;
    }

    const selectedType = typeSelect.value;
    const filteredScopes = knownLogScopes.filter((scope) => !selectedType || scope.type === selectedType);
    const currentID = activeLogFilter.scopeID;

    idSelect.innerHTML = '<option value="">All channel/video entries</option>' + filteredScopes.map((scope) =>
        `<option value="${scope.id}">${escapeHtml(scope.type)}: ${escapeHtml(scope.name || scope.id)}</option>`
    ).join('');

    if (currentID && filteredScopes.some((scope) => scope.id === currentID)) {
        idSelect.value = currentID;
    } else {
        idSelect.value = '';
        activeLogFilter.scopeID = '';
    }
}

function openScopedLogs(scopeType, scopeID, scopeName) {
    activeLogFilter.scopeType = scopeType;
    activeLogFilter.scopeID = scopeID;
    if (!knownLogScopes.some((scope) => scope.type === scopeType && scope.id === scopeID)) {
        knownLogScopes.push({ type: scopeType, id: scopeID, name: scopeName });
    }
    const logsTabTrigger = document.querySelector('#logs-tab');
    if (logsTabTrigger) {
        const tab = new bootstrap.Tab(logsTabTrigger);
        tab.show();
    }
    updateLogScopeOptions();
    loadLogs();
}

// Save pasted cookies with confirmation
document.getElementById('saveCookiesBtn').addEventListener('click', async () => {
    const cookieText = document.getElementById('cookiesPaste').value.trim();
    if (!cookieText) {
        showToast('Please paste some cookies first', true);
        return;
    }

    // Check if cookies already exist
    let confirmMessage = 'Save these cookies?';
    try {
        const response = await fetch(`${API_BASE}/config`);
        const data = await response.json();
        if (data.data && data.data.yt_dlp && data.data.yt_dlp.cookies_file) {
            confirmMessage = 'This will overwrite your existing cookies. Continue?';
        }
    } catch (e) {
        // Continue regardless
    }

    if (!confirm(confirmMessage)) {
        return;
    }

    try {
        const response = await fetch(`${API_BASE}/cookies`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ cookie_text: cookieText })
        });
        const data = await response.json();
        if (data.success) {
            showToast('Cookies saved successfully!');
            document.getElementById('cookiesPaste').value = '';
            loadConfig();
        } else {
            showToast(data.message || 'Failed to save cookies', true);
        }
    } catch (error) {
        showToast('Failed to save cookies', true);
    }
});

// Clear all cookies
document.getElementById('clearCookiesBtn').addEventListener('click', async () => {
    if (!confirm('Are you sure you want to clear all cookies? This cannot be undone.')) {
        return;
    }

    try {
        const response = await fetch(`${API_BASE}/cookies/clear`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' }
        });
        const data = await response.json();
        if (data.success) {
            showToast('All cookies cleared!');
            document.getElementById('cookiesPaste').value = '';
            loadConfig();
        } else {
            showToast(data.message || 'Failed to clear cookies', true);
        }
    } catch (error) {
        showToast('Failed to clear cookies', true);
    }
});

// Add channel
document.getElementById('addChannelForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const form = e.currentTarget;
    const name = document.getElementById('channelName').value;
    const url = document.getElementById('channelURL').value;
    const retention = parseInt(document.getElementById('channelRetention').value) || 0;
    const cutoffDate = document.getElementById('channelCutoffDate').value;
    const quality = document.getElementById('channelQuality').value;
    const format = document.getElementById('channelFormat').value; // Empty string = use global default
    const downloadShorts = document.getElementById('channelDownloadShorts').checked;
    const disablePruning = document.getElementById('channelDisablePruning').checked;
    const skipAutoDownload = !document.getElementById('channelAutoDownload').checked;
    const disableNotifications = !document.getElementById('channelNotify').checked;

    const channelData = {
        name,
        url,
        retention_days: retention,
        disable_pruning: disablePruning,
        video_quality: quality,
        video_format: format,
        download_shorts: downloadShorts,
        skip_auto_download: skipAutoDownload,
        disable_notifications: disableNotifications,
    };
    if (cutoffDate) {
        channelData.cutoff_date = new Date(cutoffDate).toISOString();
    }

    setFormBusy(form, true);
    try {
        const response = await fetch(`${API_BASE}/channels`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(channelData)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Channel added successfully');
            resetAddForm(form);
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add channel', true);
        }
    } catch (error) {
        showToast('Failed to add channel', true);
    } finally {
        setFormBusy(form, false);
    }
});

// Remove channel
async function removeChannel(id) {
    if (!confirm('Are you sure you want to remove this channel?')) return;

    try {
        const response = await fetch(`${API_BASE}/channels/${id}`, { method: 'DELETE' });
        const data = await response.json();
        if (data.success) {
            showToast('Channel removed successfully');
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to remove channel', true);
        }
    } catch (error) {
        showToast('Failed to remove channel', true);
    }
}

// Toggle pruning for a downloaded video in a channel
async function setChannelVideoPruning(channelId, videoId, disablePruning) {
    try {
        const response = await fetch(`${API_BASE}/channels/${channelId}/videos/${videoId}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ disable_pruning: disablePruning })
        });
        const data = await response.json();
        if (data.success) {
            showToast(disablePruning ? 'Video will be kept indefinitely' : 'Video pruning re-enabled');
            await loadChannels();
        } else {
            showToast(data.message || 'Failed to update video pruning setting', true);
        }
    } catch (error) {
        showToast('Failed to update video pruning setting', true);
    }
}

// Open edit channel modal
function openEditChannelModal(id) {
    const ch = _channelsByID[id];
    if (!ch) {
        showToast('Channel data not found, please refresh', true);
        return;
    }
    const { name, retention_days: retentionDays, disable_pruning: disablePruning, cutoff_date: cutoffDate,
        video_quality: videoQuality, video_format: videoFormat, download_shorts: downloadShorts,
        skip_auto_download: skipAutoDownload, disable_notifications: disableNotifications } = ch;
    document.getElementById('editChannelId').value = id;
    document.getElementById('editChannelName').value = name;
    document.getElementById('editChannelRetention').value = retentionDays || '';
    document.getElementById('editChannelDisablePruning').checked = !!disablePruning;

    // Convert ISO date to YYYY-MM-DD format for date input
    if (cutoffDate && cutoffDate !== '0001-01-01T00:00:00Z') {
        const date = new Date(cutoffDate);
        document.getElementById('editChannelCutoffDate').value = date.toISOString().split('T')[0];
    } else {
        document.getElementById('editChannelCutoffDate').value = '';
    }

    document.getElementById('editChannelQuality').value = videoQuality || '';
    document.getElementById('editChannelFormat').value = videoFormat || '';
    document.getElementById('editChannelDownloadShorts').checked = downloadShorts || false;
    document.getElementById('editChannelAutoDownload').checked = !skipAutoDownload;
    document.getElementById('editChannelNotify').checked = !disableNotifications;

    const modal = new bootstrap.Modal(document.getElementById('editChannelModal'));
    modal.show();
}

// Save channel edits
document.getElementById('saveChannelEditsBtn').addEventListener('click', async () => {
    const id = document.getElementById('editChannelId').value;
    const retentionDays = parseInt(document.getElementById('editChannelRetention').value) || 0;
    const disablePruning = document.getElementById('editChannelDisablePruning').checked;
    const cutoffDate = document.getElementById('editChannelCutoffDate').value;
    const videoQuality = document.getElementById('editChannelQuality').value;
    const videoFormat = document.getElementById('editChannelFormat').value; // Empty string = use global default
    const downloadShorts = document.getElementById('editChannelDownloadShorts').checked;
    const skipAutoDownload = !document.getElementById('editChannelAutoDownload').checked;
    const disableNotifications = !document.getElementById('editChannelNotify').checked;

    const updateData = {
        retention_days: retentionDays,
        disable_pruning: disablePruning,
        video_quality: videoQuality,
        video_format: videoFormat,
        download_shorts: downloadShorts,
        skip_auto_download: skipAutoDownload,
        disable_notifications: disableNotifications,
    };

    // Only include cutoff date if it's set
    if (cutoffDate) {
        updateData.cutoff_date = new Date(cutoffDate).toISOString();
    } else {
        updateData.cutoff_date = '0001-01-01T00:00:00Z';
    }

    try {
        const response = await fetch(`${API_BASE}/channels/${id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(updateData)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Channel updated successfully');
            bootstrap.Modal.getInstance(document.getElementById('editChannelModal')).hide();
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to update channel', true);
        }
    } catch (error) {
        showToast('Failed to update channel', true);
    }
});

// Add video
document.getElementById('addVideoForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    const form = e.currentTarget;
    const url = document.getElementById('videoURL').value;
    const retention = parseInt(document.getElementById('videoRetention').value) || 0;
    const quality = document.getElementById('videoQuality').value;
    const format = document.getElementById('videoFormat').value; // Empty string = use global default
    const disablePruning = document.getElementById('videoDisablePruning').checked;

    setFormBusy(form, true);
    try {
        const response = await fetch(`${API_BASE}/videos`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                url,
                retention_days: retention,
                disable_pruning: disablePruning,
                video_quality: quality,
                video_format: format,
            })
        });
        const data = await response.json();
        if (data.success) {
            showToast('Video added successfully');
            resetAddForm(form);
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add video', true);
        }
    } catch (error) {
        showToast('Failed to add video', true);
    } finally {
        setFormBusy(form, false);
    }
});

// Remove video
async function removeVideo(id) {
    if (!confirm('Are you sure you want to remove this video?')) return;

    try {
        const response = await fetch(`${API_BASE}/videos/${id}`, { method: 'DELETE' });
        const data = await response.json();
        if (data.success) {
            showToast('Video removed successfully');
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to remove video', true);
        }
    } catch (error) {
        showToast('Failed to remove video', true);
    }
}

// Open edit video modal
function openEditVideoModal(id) {
    const vid = _videosByID[id];
    if (!vid) {
        showToast('Video data not found, please refresh', true);
        return;
    }
    document.getElementById('editVideoId').value = id;
    document.getElementById('editVideoTitle').value = vid.title || '';
    document.getElementById('editVideoRetention').value = vid.retention_days || '';
    document.getElementById('editVideoDisablePruning').checked = !!vid.disable_pruning;
    document.getElementById('editVideoQuality').value = vid.video_quality || '';
    document.getElementById('editVideoFormat').value = vid.video_format || '';

    const modal = new bootstrap.Modal(document.getElementById('editVideoModal'));
    modal.show();
}

// Save video edits
document.getElementById('saveVideoEditsBtn').addEventListener('click', async () => {
    const id = document.getElementById('editVideoId').value;
    const retentionDays = parseInt(document.getElementById('editVideoRetention').value) || 0;
    const disablePruning = document.getElementById('editVideoDisablePruning').checked;
    const videoQuality = document.getElementById('editVideoQuality').value;
    const videoFormat = document.getElementById('editVideoFormat').value;

    const updateData = {
        retention_days: retentionDays,
        disable_pruning: disablePruning,
        video_quality: videoQuality,
        video_format: videoFormat,
    };

    try {
        const response = await fetch(`${API_BASE}/videos/${id}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(updateData)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Video updated successfully');
            bootstrap.Modal.getInstance(document.getElementById('editVideoModal')).hide();
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to update video', true);
        }
    } catch (error) {
        showToast('Failed to update video', true);
    }
});

// Update config
document.getElementById('configForm').addEventListener('submit', async (e) => {
    e.preventDefault();
    
    // Convert HMS to Go duration strings
    const checkIntervalStr = hmsToGoDuration(
        document.getElementById('checkIntervalH').value,
        document.getElementById('checkIntervalM').value,
        document.getElementById('checkIntervalS').value
    );
    const updateIntervalStr = hmsToGoDuration(
        document.getElementById('ytDlpUpdateIntervalH').value,
        document.getElementById('ytDlpUpdateIntervalM').value,
        document.getElementById('ytDlpUpdateIntervalS').value
    );
    const sleepIntervalStr = hmsToGoDuration(
        document.getElementById('extractorSleepIntervalH').value,
        document.getElementById('extractorSleepIntervalM').value,
        document.getElementById('extractorSleepIntervalS').value
    );
    
    const config = {
        check_interval_seconds: checkIntervalStr,
        retention_days: parseInt(document.getElementById('retentionDays').value),
        disable_pruning: document.getElementById('disablePruning').checked,
        download_dir: document.getElementById('downloadDir').value,
        media_server_download_dir: document.getElementById('mediaServerDownloadDir').value.trim(),
        file_name_pattern: document.getElementById('fileNamePattern').value,
        max_concurrent_downloads: parseInt(document.getElementById('maxConcurrent').value),
        max_log_entries: parseInt(document.getElementById('maxLogEntries').value) || 100,
        default_video_format: document.getElementById('defaultVideoFormat').value || 'mp4',
        default_video_quality: document.getElementById('defaultVideoQuality').value,
        yt_dlp: {
            update_interval_seconds: updateIntervalStr,
            extractor_sleep_interval_seconds: sleepIntervalStr,
            download_throughput_limit: document.getElementById('downloadThroughputLimit').value.trim(),
            restrict_filenames: document.getElementById('restrictFilenames').checked,
            cache_dir: document.getElementById('ytDlpCacheDir').value.trim(),
            cookies_browser: document.getElementById('cookiesBrowser').value,
        },
        subtitles: {
            enabled: document.getElementById('subtitlesEnabled').checked,
            languages: document.getElementById('subtitleLanguages').value.trim() || 'en',
            auto_generated: document.getElementById('subtitlesAutoGenerated').checked,
        },
    };

    try {
        config.notifications = collectNotificationSettings();
    } catch (error) {
        showToast(error.message, true);
        return;
    }

    try {
        const response = await fetch(`${API_BASE}/config`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(config)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Configuration saved successfully');
        } else {
            showToast(data.message || 'Failed to save configuration', true);
        }
    } catch (error) {
        showToast('Failed to save configuration', true);
    }
});

document.getElementById('refreshLogsBtn').addEventListener('click', () => {
    loadLogs();
});

document.getElementById('logsScopeType').addEventListener('change', (e) => {
    activeLogFilter.scopeType = e.target.value;
    activeLogFilter.scopeID = '';
    updateLogScopeOptions();
    loadLogs();
});

document.getElementById('logsScopeId').addEventListener('change', (e) => {
    activeLogFilter.scopeID = e.target.value;
    loadLogs();
});

document.getElementById('clearLogFilterBtn').addEventListener('click', () => {
    activeLogFilter.scopeType = '';
    activeLogFilter.scopeID = '';
    updateLogScopeOptions();
    loadLogs();
});

// ---- Download queue ----

function formatUptime(totalSeconds) {
    const s = Math.max(0, Math.floor(totalSeconds));
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
}

function formatBytes(bytes) {
    if (!bytes || bytes <= 0) return '';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let value = bytes;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
        value /= 1024;
        unit++;
    }
    return `${value.toFixed(value >= 100 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatEta(seconds) {
    if (seconds == null || seconds < 0) return '';
    if (seconds < 60) return `${seconds}s left`;
    const m = Math.floor(seconds / 60);
    if (m < 60) return `${m}m ${seconds % 60}s left`;
    return `${Math.floor(m / 60)}h ${m % 60}m left`;
}

const DOWNLOAD_STAGE_LABELS = { video: 'Downloading video', audio: 'Downloading audio', media: 'Downloading', subtitles: 'Downloading subtitles' };

function renderDownloadItem(d) {
    const scope = d.scope_name ? `<span class="text-muted ms-2 small">${escapeHtml(d.scope_name)}</span>` : '';
    const manual = d.manual ? '<span class="badge bg-secondary ms-2">Manual</span>' : '';
    if (d.phase === 'queued') {
        return `
            <div class="download-item d-flex justify-content-between align-items-center py-1">
                <div class="text-truncate"><i class="bi bi-hourglass me-2 text-muted"></i>${escapeHtml(d.title || d.video_id)}${scope}</div>
                <span class="badge bg-dark ms-2">Queued</span>
            </div>
        `;
    }
    let label;
    let details = '';
    let barClass = 'progress-bar';
    let width = 100;
    if (d.phase === 'processing') {
        label = d.stage ? d.stage.charAt(0).toUpperCase() + d.stage.slice(1) : 'Processing';
        barClass += ' progress-bar-striped progress-bar-animated bg-info';
    } else if (d.percent == null) {
        label = DOWNLOAD_STAGE_LABELS[d.stage] || 'Starting';
        barClass += ' progress-bar-striped progress-bar-animated';
    } else {
        label = DOWNLOAD_STAGE_LABELS[d.stage] || 'Downloading';
        width = Math.max(0, Math.min(100, d.percent));
        const parts = [`${width.toFixed(0)}%`];
        if (d.total_bytes > 0) parts.push(`${formatBytes(d.downloaded_bytes)} of ${formatBytes(d.total_bytes)}`);
        if (d.speed_bps > 0) parts.push(`${formatBytes(d.speed_bps)}/s`);
        const eta = formatEta(d.eta_seconds);
        if (eta) parts.push(eta);
        details = parts.join(' · ');
    }
    return `
        <div class="download-item py-2">
            <div class="d-flex justify-content-between align-items-center">
                <div class="text-truncate"><i class="bi bi-arrow-down-circle me-2 text-success"></i><strong>${escapeHtml(d.title || d.video_id)}</strong>${scope}${manual}</div>
                <small class="text-muted ms-2 text-nowrap">${escapeHtml(label)}</small>
            </div>
            <div class="progress mt-1" role="progressbar" aria-label="${attr(label)}" aria-valuenow="${width.toFixed(0)}" aria-valuemin="0" aria-valuemax="100">
                <div class="${barClass}" style="width: ${width}%"></div>
            </div>
            ${details ? `<small class="text-muted">${escapeHtml(details)}</small>` : ''}
        </div>
    `;
}

let _lastDownloadsKey = null;
let _lastDownloadIDs = new Set();
let _downloadsTimer = null;

async function loadDownloads() {
    clearTimeout(_downloadsTimer);
    let items = [];
    try {
        const response = await fetch(`${API_BASE}/downloads`);
        const data = await response.json();
        if (data.success) items = data.data || [];
    } catch (error) {
        // Keep showing the last state; the next poll will retry.
        items = null;
    }

    if (items) {
        const key = JSON.stringify(items);
        if (key !== _lastDownloadsKey) {
            _lastDownloadsKey = key;
            const card = document.getElementById('downloadsCard');
            card.classList.toggle('d-none', items.length === 0);
            const active = items.filter((d) => d.phase !== 'queued');
            const queued = items.filter((d) => d.phase === 'queued');
            document.getElementById('downloadsCount').textContent = items.length
                ? `${active.length} active${queued.length ? `, ${queued.length} queued` : ''}`
                : '';
            document.getElementById('downloadsList').innerHTML = items.map(renderDownloadItem).join('');
        }

        // When a download leaves the list it has finished (or failed), so refresh the
        // channel and video lists to show the result.
        const ids = new Set(items.map((d) => d.video_id));
        const finished = [..._lastDownloadIDs].some((id) => !ids.has(id));
        _lastDownloadIDs = ids;
        if (finished) {
            loadStatus();
            loadChannels().then(loadVideos);
        }
    }

    // Poll quickly while something is downloading, slowly otherwise.
    const busy = items ? items.length > 0 : _lastDownloadIDs.size > 0;
    _downloadsTimer = setTimeout(loadDownloads, busy ? 2000 : 15000);
}

// Initial load — channels must complete first so _trackedChannelIDs is populated
// before videos are rendered (prevents missing Move/Convert buttons on first paint).
loadStatus();
loadChannels().then(loadVideos);
loadConfig();
loadDatabaseInfo();
loadLogs();
loadDownloads();

// Refresh data every 30 seconds
setInterval(() => {
    loadStatus();
    loadChannels().then(loadVideos);
    loadLogs();
}, 30000);


// ---- Notifications settings ----

const NOTIFICATION_EVENT_LABELS = {
    'video.downloaded': 'New video downloaded',
    'video.pending': 'New video waiting (no auto-download)',
    'download.failed': 'Download failed',
    'cookies.expired': 'Cookies need attention',
    'channel.check_failed': 'Channel check failed',
};
let notificationEvents = Object.keys(NOTIFICATION_EVENT_LABELS);

function loadNotificationSettings(notifications, events) {
    if (events.length) {
        notificationEvents = events;
    }
    document.getElementById('notifyIndividualVideos').checked = notifications.individual_videos !== false;
    document.getElementById('notifyAppURL').value = notifications.app_url || '';
    document.getElementById('notifyRepeatCooldown').value = notifications.repeat_cooldown || '6h0m0s';
    document.getElementById('notifyPendingThreshold').value = notifications.pending_summary_threshold || 5;
    const list = document.getElementById('webhooksList');
    list.innerHTML = '';
    (notifications.webhooks || []).forEach(hook => list.appendChild(renderWebhookCard(hook)));
    updateWebhookEmptyState();
}

function updateWebhookEmptyState() {
    const list = document.getElementById('webhooksList');
    const empty = list.querySelector('.webhook-empty');
    const hasCards = list.querySelector('.webhook-card') !== null;
    if (!hasCards && !empty) {
        list.insertAdjacentHTML('beforeend', '<p class="text-muted webhook-empty mb-2">No webhooks yet. Add an ntfy topic or a generic JSON endpoint.</p>');
    } else if (hasCards && empty) {
        empty.remove();
    }
}

// Escapes a value for use inside a double- or single-quoted HTML attribute.
function attr(value) {
    return escapeHtml(value == null ? '' : String(value)).replace(/"/g, '&quot;').replace(/'/g, '&#39;');
}

function renderActionRow(action = {}) {
    const row = document.createElement('div');
    row.className = 'row g-2 align-items-center mb-2 action-row';
    const type = action.action || 'view';
    row.innerHTML = `
        <div class="col-md-2">
            <select class="form-select form-select-sm" data-field="action">
                <option value="view" ${type === 'view' ? 'selected' : ''}>Open URL</option>
                <option value="http" ${type === 'http' ? 'selected' : ''}>HTTP request</option>
            </select>
        </div>
        <div class="col-md-2"><input type="text" class="form-control form-control-sm" data-field="label" placeholder="Label" value="${attr(action.label)}"></div>
        <div class="col-md-5"><input type="text" class="form-control form-control-sm" data-field="url" placeholder="https://... or app deeplink, e.g. vnd.youtube://{{.VideoID}}" value="${attr(action.url)}"></div>
        <div class="col-md-1"><input type="text" class="form-control form-control-sm" data-field="method" placeholder="POST" title="HTTP method (HTTP request actions)" value="${attr(action.method)}"></div>
        <div class="col-md-1">
            <div class="form-check" title="Dismiss the notification after tapping">
                <input class="form-check-input" type="checkbox" data-field="clear" ${action.clear ? 'checked' : ''}>
                <label class="form-check-label small">Clear</label>
            </div>
        </div>
        <div class="col-md-1 text-end"><button type="button" class="btn btn-sm btn-outline-danger" data-act="remove-action" title="Remove button"><i class="bi bi-x-lg"></i></button></div>`;
    // Keep body/headers/extras authored outside the UI (config.json) intact on save.
    row.dataset.extra = JSON.stringify({ body: action.body, headers: action.headers, extras: action.extras });
    const typeSelect = row.querySelector('[data-field="action"]');
    const methodInput = row.querySelector('[data-field="method"]');
    const syncMethod = () => {
        methodInput.disabled = typeSelect.value !== 'http';
        if (methodInput.disabled) methodInput.value = '';
    };
    syncMethod();
    typeSelect.addEventListener('change', syncMethod);
    return row;
}

function renderWebhookCard(hook = {}) {
    const card = document.createElement('div');
    const type = hook.type || 'ntfy';
    const tmpl = hook.template || {};
    const subscribed = hook.events && hook.events.length ? hook.events : notificationEvents;
    card.className = 'webhook-card' + (hook.disabled ? ' disabled-hook' : '');
    card.innerHTML = `
        <div class="row g-2">
            <div class="col-md-3">
                <label class="form-label small mb-1">Name</label>
                <input type="text" class="form-control form-control-sm" data-field="name" placeholder="Phone" value="${attr(hook.name)}">
            </div>
            <div class="col-md-2">
                <label class="form-label small mb-1">Type</label>
                <select class="form-select form-select-sm" data-field="type">
                    <option value="ntfy" ${type === 'ntfy' ? 'selected' : ''}>ntfy</option>
                    <option value="json" ${type === 'json' ? 'selected' : ''}>JSON POST</option>
                </select>
            </div>
            <div class="col-md-5">
                <label class="form-label small mb-1">URL</label>
                <input type="url" class="form-control form-control-sm" data-field="url" placeholder="${type === 'ntfy' ? 'https://ntfy.sh/your-topic' : 'https://example.com/hook'}" value="${attr(hook.url)}">
            </div>
            <div class="col-md-2 d-flex align-items-end">
                <div class="form-check form-switch">
                    <input class="form-check-input" type="checkbox" data-field="enabled" ${hook.disabled ? '' : 'checked'}>
                    <label class="form-check-label small">Enabled</label>
                </div>
            </div>

            <div class="col-12">
                <label class="form-label small mb-1">Events</label>
                <div class="d-flex flex-wrap gap-3">
                    ${notificationEvents.map(ev => `
                        <div class="form-check">
                            <input class="form-check-input" type="checkbox" data-event="${attr(ev)}" ${subscribed.includes(ev) ? 'checked' : ''}>
                            <label class="form-check-label small" title="${attr(ev)}">${escapeHtml(NOTIFICATION_EVENT_LABELS[ev] || ev)}</label>
                        </div>`).join('')}
                </div>
            </div>

            <div class="col-md-4">
                <label class="form-label small mb-1">Access token</label>
                <input type="password" class="form-control form-control-sm" data-field="token" autocomplete="off" placeholder="Bearer token (optional)" value="${attr(hook.token)}">
            </div>
            <div class="col-md-4">
                <label class="form-label small mb-1">Username</label>
                <input type="text" class="form-control form-control-sm" data-field="username" autocomplete="off" value="${attr(hook.username)}">
            </div>
            <div class="col-md-4">
                <label class="form-label small mb-1">Password</label>
                <input type="password" class="form-control form-control-sm" data-field="password" autocomplete="off" value="${attr(hook.password)}">
            </div>

            <div class="col-md-4 ntfy-only">
                <label class="form-label small mb-1" title="The image is picked automatically: the video's thumbnail, or the channel's picture when there's no video">Image</label>
                <select class="form-select form-select-sm" data-field="attachment">
                    <option value="url" ${!hook.attachment || hook.attachment === 'url' ? 'selected' : ''}>Automatic (video thumbnail, else channel picture)</option>
                    <option value="upload" ${hook.attachment === 'upload' ? 'selected' : ''}>Automatic, uploading the downloaded thumbnail</option>
                    <option value="none" ${hook.attachment === 'none' ? 'selected' : ''}>No image</option>
                </select>
            </div>
            <div class="col-md-2 ntfy-only">
                <label class="form-label small mb-1">Priority</label>
                <select class="form-select form-select-sm" data-field="priority">
                    <option value="0" ${!tmpl.priority ? 'selected' : ''}>Per event</option>
                    ${[1, 2, 3, 4, 5].map(p => `<option value="${p}" ${tmpl.priority === p ? 'selected' : ''}>${p}</option>`).join('')}
                </select>
            </div>
            <div class="col-md-4 ntfy-only">
                <label class="form-label small mb-1">Tags / emoji</label>
                <input type="text" class="form-control form-control-sm" data-field="tags" placeholder="Per event, e.g. tv,youtube" value="${attr((tmpl.tags || []).join(','))}">
            </div>
            <div class="col-md-2 ntfy-only d-flex align-items-end">
                <div class="form-check">
                    <input class="form-check-input" type="checkbox" data-field="markdown" ${tmpl.markdown ? 'checked' : ''}>
                    <label class="form-check-label small">Markdown</label>
                </div>
            </div>

            <div class="col-md-6">
                <label class="form-label small mb-1">Title template</label>
                <input type="text" class="form-control form-control-sm" data-field="title" placeholder="Per event, e.g. {{.ChannelName}}" value="${attr(tmpl.title)}">
            </div>
            <div class="col-md-6">
                <label class="form-label small mb-1">Message template</label>
                <input type="text" class="form-control form-control-sm" data-field="message" placeholder="Per event, e.g. {{.Title}}" value="${attr(tmpl.message)}">
            </div>
            <div class="col-12 ntfy-only">
                <label class="form-label small mb-1">Tap action (URL or deeplink)</label>
                <input type="text" class="form-control form-control-sm" data-field="click" placeholder="Per event, e.g. {{.VideoURL}}" value="${attr(tmpl.click)}">
            </div>

            <div class="col-12 ntfy-only">
                <div class="d-flex justify-content-between align-items-center mb-1">
                    <label class="form-label small mb-0">Action buttons (up to 3)</label>
                    <button type="button" class="btn btn-sm btn-outline-secondary" data-act="add-action"><i class="bi bi-plus-lg"></i> Button</button>
                </div>
                <div class="actions-list"></div>
            </div>

            <div class="col-12">
                <details>
                    <summary class="small text-muted">Advanced: custom headers and per-event overrides (JSON)</summary>
                    <div class="row g-2 mt-1">
                        <div class="col-md-4">
                            <label class="form-label small mb-1">Headers</label>
                            <textarea class="form-control" rows="4" data-field="headers" placeholder='{"X-Api-Key": "..."}'>${hook.headers && Object.keys(hook.headers).length ? escapeHtml(JSON.stringify(hook.headers, null, 2)) : ''}</textarea>
                        </div>
                        <div class="col-md-8">
                            <label class="form-label small mb-1">Per-event overrides</label>
                            <textarea class="form-control" rows="4" data-field="event_templates" placeholder='{"cookies.expired": {"priority": 5, "tags": ["rotating_light"]}}'>${hook.event_templates && Object.keys(hook.event_templates).length ? escapeHtml(JSON.stringify(hook.event_templates, null, 2)) : ''}</textarea>
                        </div>
                    </div>
                </details>
            </div>

            <div class="col-12 d-flex justify-content-end gap-2">
                <span class="small align-self-center me-auto test-result"></span>
                <button type="button" class="btn btn-sm btn-outline-info" data-act="test"><i class="bi bi-send"></i> Send test</button>
                <button type="button" class="btn btn-sm btn-outline-danger" data-act="remove"><i class="bi bi-trash"></i> Remove</button>
            </div>
        </div>`;

    card.dataset.icon = tmpl.icon || '';
    const actionsList = card.querySelector('.actions-list');
    (tmpl.actions || []).forEach(a => actionsList.appendChild(renderActionRow(a)));

    const syncType = () => {
        const isNtfy = card.querySelector('[data-field="type"]').value === 'ntfy';
        card.querySelectorAll('.ntfy-only').forEach(el => el.classList.toggle('d-none', !isNtfy));
    };
    syncType();
    card.querySelector('[data-field="type"]').addEventListener('change', syncType);
    card.querySelector('[data-field="enabled"]').addEventListener('change', (e) => {
        card.classList.toggle('disabled-hook', !e.target.checked);
    });

    card.addEventListener('click', async (e) => {
        const btn = e.target.closest('[data-act]');
        if (!btn) return;
        const act = btn.dataset.act;
        if (act === 'remove') {
            card.remove();
            updateWebhookEmptyState();
        } else if (act === 'add-action') {
            if (actionsList.children.length >= 3) {
                showToast('ntfy supports at most 3 action buttons', true);
                return;
            }
            actionsList.appendChild(renderActionRow());
        } else if (act === 'remove-action') {
            btn.closest('.action-row').remove();
        } else if (act === 'test') {
            await testWebhookCard(card, btn);
        }
    });
    return card;
}

function parseJSONField(card, field, label) {
    const raw = card.querySelector(`[data-field="${field}"]`).value.trim();
    if (!raw) return undefined;
    try {
        const parsed = JSON.parse(raw);
        if (typeof parsed !== 'object' || Array.isArray(parsed) || parsed === null) throw new Error();
        return parsed;
    } catch {
        throw new Error(`${label} must be a JSON object`);
    }
}

function collectWebhookCard(card) {
    const val = (field) => card.querySelector(`[data-field="${field}"]`).value.trim();
    const name = val('name') || val('url') || 'webhook';
    const checkedEvents = [...card.querySelectorAll('[data-event]')].filter(cb => cb.checked).map(cb => cb.dataset.event);
    if (checkedEvents.length === 0) {
        throw new Error(`Webhook "${name}": select at least one event`);
    }
    const actions = [...card.querySelectorAll('.action-row')].map(row => {
        const get = (f) => row.querySelector(`[data-field="${f}"]`);
        const extra = JSON.parse(row.dataset.extra || '{}');
        const action = {
            action: get('action').value,
            label: get('label').value.trim(),
            url: get('url').value.trim(),
            clear: get('clear').checked,
            ...Object.fromEntries(Object.entries(extra).filter(([, v]) => v != null)),
        };
        const method = get('method').value.trim();
        if (method) action.method = method.toUpperCase();
        return action;
    }).filter(a => a.label || a.url);

    const tags = val('tags').split(',').map(t => t.trim()).filter(Boolean);
    const template = {
        title: val('title') || undefined,
        message: val('message') || undefined,
        click: val('click') || undefined,
        icon: card.dataset.icon || undefined,
        priority: parseInt(val('priority')) || undefined,
        tags: tags.length ? tags : undefined,
        markdown: card.querySelector('[data-field="markdown"]').checked || undefined,
        actions: actions.length ? actions : undefined,
    };

    return {
        name: val('name'),
        type: val('type'),
        url: val('url'),
        disabled: !card.querySelector('[data-field="enabled"]').checked,
        // Every box ticked means "all events", which also covers events added later.
        events: checkedEvents.length === notificationEvents.length ? [] : checkedEvents,
        token: val('token') || undefined,
        username: val('username') || undefined,
        password: card.querySelector('[data-field="password"]').value || undefined,
        attachment: val('type') === 'ntfy' ? val('attachment') : undefined,
        headers: parseJSONField(card, 'headers', `Webhook "${name}" headers`),
        template,
        event_templates: parseJSONField(card, 'event_templates', `Webhook "${name}" per-event overrides`),
    };
}

function collectNotificationSettings() {
    const threshold = parseInt(document.getElementById('notifyPendingThreshold').value);
    return {
        individual_videos: document.getElementById('notifyIndividualVideos').checked,
        app_url: document.getElementById('notifyAppURL').value.trim(),
        repeat_cooldown: document.getElementById('notifyRepeatCooldown').value.trim() || '6h0m0s',
        pending_summary_threshold: threshold > 0 ? threshold : 5,
        webhooks: [...document.querySelectorAll('#webhooksList .webhook-card')].map(collectWebhookCard),
    };
}

async function testWebhookCard(card, btn) {
    const result = card.querySelector('.test-result');
    let webhook;
    try {
        webhook = collectWebhookCard(card);
    } catch (error) {
        showToast(error.message, true);
        return;
    }
    btn.disabled = true;
    result.className = 'small align-self-center me-auto test-result text-muted';
    result.textContent = 'Sending...';
    try {
        const response = await fetch(`${API_BASE}/notifications/test`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ webhook }),
        });
        const data = await response.json();
        const r = data.success && data.data.results && data.data.results[0];
        if (r && r.ok) {
            result.className = 'small align-self-center me-auto test-result text-success';
            result.textContent = 'Test notification sent';
        } else {
            result.className = 'small align-self-center me-auto test-result text-danger';
            result.textContent = (r && r.error) || data.message || 'Test failed';
        }
    } catch (error) {
        result.className = 'small align-self-center me-auto test-result text-danger';
        result.textContent = 'Test failed';
    } finally {
        btn.disabled = false;
    }
}

document.getElementById('addNtfyWebhookBtn').addEventListener('click', () => {
    document.getElementById('webhooksList').appendChild(renderWebhookCard({ type: 'ntfy' }));
    updateWebhookEmptyState();
});
document.getElementById('addJSONWebhookBtn').addEventListener('click', () => {
    document.getElementById('webhooksList').appendChild(renderWebhookCard({ type: 'json' }));
    updateWebhookEmptyState();
});


// ---- Database migration ----

let databaseInfo = null;

function dbLabel(d) {
    return d === 'postgres' ? 'PostgreSQL' : 'SQLite';
}

async function loadDatabaseInfo() {
    try {
        const response = await fetch(`${API_BASE}/database`);
        const data = await response.json();
        if (data.success) {
            databaseInfo = data.data;
            renderDatabaseInfo();
        }
    } catch (error) {
        document.getElementById('databaseInfo').textContent = 'Failed to load database status';
    }
}

function renderDatabaseInfo() {
    const info = databaseInfo;
    const lines = [];
    const where = info.active === 'postgres' ? info.postgres : info.active_path;
    lines.push(`<div><strong>Active:</strong> ${escapeHtml(dbLabel(info.active))}${where ? ` <span class="text-muted">${escapeHtml(where)}</span>` : ''}${info.fallback ? ' <span class="badge bg-warning text-dark ms-1">Temporary fallback</span>' : ''}</div>`);
    if (info.active !== 'postgres') {
        if (info.postgres_configured) {
            lines.push(`<div><strong>PostgreSQL:</strong> <span class="text-muted">${escapeHtml(info.postgres)}</span></div>`);
        } else {
            lines.push('<div><strong>PostgreSQL:</strong> <span class="text-muted">not configured (set YTDM_POSTGRES_DATABASE and the other YTDM_POSTGRES_* variables, then restart)</span></div>');
        }
    } else {
        lines.push(`<div><strong>SQLite file:</strong> <span class="text-muted">${escapeHtml(info.sqlite_path)}</span></div>`);
    }
    if (info.postgres_error) {
        lines.push(`<div class="text-danger"><i class="bi bi-x-circle me-1"></i>${escapeHtml(info.postgres_error)}</div>`);
    }
    if (info.warning) {
        lines.push(`<div class="text-warning"><i class="bi bi-exclamation-triangle me-1"></i>${escapeHtml(info.warning)}</div>`);
    }
    if (info.notice) {
        lines.push(`<div class="text-info"><i class="bi bi-info-circle me-1"></i>${escapeHtml(info.notice)}</div>`);
    }
    document.getElementById('databaseInfo').innerHTML = lines.join('');

    const pgBtn = document.getElementById('migrateToPostgresBtn');
    const sqliteBtn = document.getElementById('migrateToSQLiteBtn');
    const hints = [];
    pgBtn.disabled = info.active === 'postgres' || !info.postgres_configured || !!info.postgres_error;
    sqliteBtn.disabled = info.active === 'sqlite';
    if (info.pinned_driver) {
        const other = info.pinned_driver === 'postgres' ? sqliteBtn : pgBtn;
        other.disabled = true;
        hints.push(`YTDM_DB_DRIVER=${info.pinned_driver} is set, so switching away from ${dbLabel(info.pinned_driver)} is disabled.`);
    }
    if (info.active === 'postgres') {
        pgBtn.innerHTML = '<i class="bi bi-check-circle"></i> Using PostgreSQL';
        sqliteBtn.innerHTML = '<i class="bi bi-arrow-right-circle"></i> Migrate to SQLite';
    } else {
        sqliteBtn.innerHTML = info.fallback ? '<i class="bi bi-exclamation-triangle"></i> On temporary SQLite fallback' : '<i class="bi bi-check-circle"></i> Using SQLite';
        pgBtn.innerHTML = '<i class="bi bi-arrow-right-circle"></i> Migrate to PostgreSQL';
    }
    hints.push('ytdm keeps running during a migration; downloads and edits pause briefly while data is copied. The old database is left untouched.');
    document.getElementById('migrateHint').textContent = hints.join(' ');
}

async function migrateDatabase(to) {
    const mode = document.querySelector('input[name="migrateMode"]:checked').value;
    const label = dbLabel(to);
    const explain = mode === 'replace'
        ? `Everything currently in ${label} will be DELETED and replaced with the data from ${dbLabel(databaseInfo.active)}.`
        : `Data from ${dbLabel(databaseInfo.active)} will be merged into ${label}, keeping anything ${label} already has.`;
    if (!confirm(`Migrate to ${label}?\n\n${explain}\n\nytdm then switches to ${label} and keeps using it after restarts.`)) {
        return;
    }
    const buttons = [document.getElementById('migrateToPostgresBtn'), document.getElementById('migrateToSQLiteBtn')];
    buttons.forEach(b => { b.disabled = true; });
    const result = document.getElementById('migrateResult');
    result.className = 'mt-3 small text-muted';
    result.textContent = `Migrating to ${label}...`;
    try {
        const response = await fetch(`${API_BASE}/database/migrate`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ to, mode }),
        });
        const data = await response.json();
        if (data.success) {
            const tables = (data.data.result && data.data.result.tables) || [];
            const pick = (name) => tables.find(t => t.table === name) || { inserted: 0, updated: 0 };
            const ch = pick('channels');
            const vid = pick('videos');
            result.className = 'mt-3 small text-success';
            result.textContent = `Now running on ${label}. Channels: ${ch.inserted} added, ${ch.updated} updated. Videos: ${vid.inserted} added, ${vid.updated} updated.`;
            showToast(`Migrated to ${label}`);
            databaseInfo = data.data.database;
            renderDatabaseInfo();
            loadStatus();
            loadChannels().then(loadVideos);
        } else {
            result.className = 'mt-3 small text-danger';
            result.textContent = data.message || 'Migration failed';
            renderDatabaseInfo();
        }
    } catch (error) {
        result.className = 'mt-3 small text-danger';
        result.textContent = 'Migration failed';
        renderDatabaseInfo();
    }
}

document.getElementById('migrateToPostgresBtn').addEventListener('click', () => migrateDatabase('postgres'));
document.getElementById('migrateToSQLiteBtn').addEventListener('click', () => migrateDatabase('sqlite'));
