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
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
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
            loadVideos();
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to convert to channel', true);
        }
    } catch (err) {
        console.error('convertToChannel error:', err);
        showToast('Failed to convert to channel', true);
    }
};

const moveToChannel = async (videoID) => {
    const vid = _currentSingletonVideos[videoID];
    if (!vid) {
        showToast('Video data not found, please refresh', true);
        return;
    }
    if (!confirm(`Move this video to its existing channel subscription "${vid.uploaderName}"?\n\nExisting video files remain on disk.`)) return;
    try {
        const response = await fetch(`${API_BASE}/videos/convert-to-channel`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                uploader_name: vid.uploaderName,
                uploader_id: vid.uploaderID,
                video_ids: [videoID],
            })
        });
        const data = await response.json();
        if (data.success) {
            showToast(`Moved video to channel "${vid.uploaderName}"`);
            loadVideos();
            loadChannels();
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
            const versionText = data.data.yt_dlp_version ? data.data.yt_dlp_version : 'unknown';
            const appCommit = data.data.app_commit_short ? data.data.app_commit_short : (data.data.app_commit || 'unknown');
            document.getElementById('status').innerHTML = `
                <span class="badge bg-success">Running</span>
                <span class="ms-3"><strong>Channels:</strong> ${data.data.channels_count}</span>
                <span class="ms-3"><strong>Videos:</strong> ${data.data.videos_count}</span>
                <span class="ms-3"><strong>yt-dlp:</strong> ${versionText}</span>
                <span class="ms-3"><strong>Commit:</strong> ${appCommit}</span>
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
        const data = await response.json();
        if (data.success) {
            const channels = (data.data || []).slice().sort((a, b) =>
                (a.name || '').localeCompare((b.name || ''), undefined, { sensitivity: 'base' })
            );
            _trackedChannelIDs = new Set(channels.map(ch => ch.id).filter(Boolean));
            if (channels.length === 0) {
                document.getElementById('channelsList').innerHTML = '<p class="text-muted">No channels configured</p>';
            } else {
                document.getElementById('channelsList').innerHTML = channels.map((ch, idx) => {
                    const cutoffText = ch.cutoff_date && ch.cutoff_date !== '0001-01-01T00:00:00Z' 
                        ? `<span class="badge bg-info ms-2">From: ${new Date(ch.cutoff_date).toLocaleDateString()}</span>` 
                        : '';
                    const qualityText = ch.video_quality 
                        ? `<span class="badge bg-primary ms-2">Quality: ${ch.video_quality}</span>`
                        : '';
                    const formatText = ch.video_format 
                        ? `<span class="badge bg-info ms-2">Format: ${ch.video_format}</span>`
                        : '';
                    const downloadedCount = (ch.downloaded_videos || []).length;
                    const collapseId = `channel-collapse-${idx}`;
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
                                <button class="btn btn-sm btn-outline-danger collapsed" type="button" data-bs-toggle="collapse" data-bs-target="#error-${idx}" aria-expanded="false">
                                    <i class="bi bi-chevron-down"></i> View Error Details
                                </button>
                                <div class="collapse mt-2" id="error-${idx}">
                                    <div class="alert alert-danger" role="alert">
                                        <strong>Error:</strong> ${ch.last_error}<br>
                                        <small class="text-muted">At: ${formatDate(ch.last_error_time)}</small>
                                    </div>
                                </div>
                            </div>
                        ` 
                        : '';

                    const downloadedVideos = (ch.downloaded_videos || []).slice().sort((a, b) =>
                        new Date(b.download_date || 0) - new Date(a.download_date || 0)
                    );
                    const childrenHtml = downloadedVideos.length === 0
                        ? '<div class="text-muted small py-2">No downloaded videos tracked for this channel yet</div>'
                        : downloadedVideos.map((video) => `
                            <div class="d-flex justify-content-between align-items-center border-bottom py-2">
                                <div style="flex-grow: 1;">
                                    <strong>${video.title || video.id}</strong>
                                    <div class="small text-muted">Downloaded: ${formatDate(video.download_date)}</div>
                                </div>
                                <div class="ms-2">
                                    <a href="https://www.youtube.com/watch?v=${video.id}" target="_blank" class="btn btn-outline-primary btn-sm me-1">
                                        <i class="bi bi-youtube"></i>
                                    </a>
                                    <button class="btn btn-sm ${video.disable_pruning ? 'btn-outline-warning' : 'btn-outline-secondary'}" onclick="setChannelVideoPruning('${ch.id}', '${video.id}', ${!video.disable_pruning})">
                                        <i class="bi ${video.disable_pruning ? 'bi-unlock' : 'bi-lock'}"></i>
                                    </button>
                                </div>
                            </div>
                        `).join('');

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
                                    ${thumbHtml}<strong>${ch.name}</strong>
                                    <span class="badge bg-secondary ms-2">${ch.retention_days || 'default'} days</span>
                                    <span class="badge bg-dark ms-2">${downloadedCount} downloaded</span>
                                    ${cutoffText}
                                    ${qualityText}
                                    ${formatText}
                                    ${pruningText}
                                    ${ch.download_shorts ? '<span class="badge bg-success ms-2">Shorts</span>' : ''}
                                    ${errorBadge}<br>
                                    <small class="text-muted">${ch.url}</small><br>
                                    <span class="last-checked">Last checked: ${formatDate(ch.last_checked)}</span>
                                    ${errorSection}
                                </div>
                                <div class="ms-3">
                                    <button class="btn btn-outline-light btn-sm me-2" onclick="openScopedLogs('channel', '${ch.id}', '${ch.name.replace(/'/g, "\\'")}')">
                                        <i class="bi bi-journal-text"></i> Logs
                                    </button>
                                    <button class="btn btn-warning btn-sm me-2" onclick="openEditChannelModal('${ch.id}', '${ch.name.replace(/'/g, "\\'")}', ${ch.retention_days}, ${ch.disable_pruning}, '${ch.cutoff_date}', '${ch.video_quality}', '${ch.video_format}', ${ch.download_shorts})">
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
            }
        }
    } catch (error) {
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
        ? `<span class="badge bg-success ms-2"><i class="bi bi-check-circle"></i> Downloaded (${downloadedCount})</span>`
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
                        <strong>Error:</strong> ${vid.last_error}<br>
                        <small class="text-muted">At: ${formatDate(vid.last_error_time)}</small>
                    </div>
                </div>
            </div>
        `
        : '';
    const moveBtn = vid.uploader_id && _trackedChannelIDs.has(vid.uploader_id)
        ? `<button class="btn btn-sm btn-outline-success me-2" onclick="moveToChannel('${vid.id}')"><i class="bi bi-collection-play-fill"></i> Move to Channel</button>`
        : '';
    return `
        <div class="d-flex justify-content-between align-items-start border-bottom py-3">
            <div style="flex-grow: 1;">
                <strong>${vid.title}</strong>
                <span class="badge bg-secondary ms-2">${vid.retention_days || 'default'} days</span>
                ${vid.video_quality ? `<span class="badge bg-primary ms-2">Quality: ${vid.video_quality}</span>` : ''}
                ${vid.video_format ? `<span class="badge bg-info ms-2">Format: ${vid.video_format}</span>` : ''}
                ${pruningText}
                ${downloadedText}
                ${errorBadge}<br>
                <small class="text-muted">${vid.url}</small><br>
                <span class="last-checked">Last checked: ${formatDate(vid.last_checked)}</span>
                ${errorSection}
            </div>
            <div class="ms-3">
                <button class="btn btn-outline-light btn-sm me-2" onclick="openScopedLogs('video', '${vid.id}', '${escapeHtml(vid.title || vid.id)}')">
                    <i class="bi bi-journal-text"></i> Logs
                </button>
                ${moveBtn}
                <button class="btn btn-warning btn-sm me-2" onclick="openEditVideoModal('${vid.id}', '${escapeHtml(vid.title)}', ${vid.retention_days}, ${vid.disable_pruning}, '${vid.video_quality}', '${vid.video_format}')">
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
            ? `<span class="badge bg-success ms-2"><i class="bi bi-check-circle"></i> Downloaded (${downloadedCount})</span>`
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
                            <strong>Error:</strong> ${vid.last_error}<br>
                            <small class="text-muted">At: ${formatDate(vid.last_error_time)}</small>
                        </div>
                    </div>
                </div>
            `
            : '';
        return `
            <div class="d-flex justify-content-between align-items-start border-bottom py-2">
                <div style="flex-grow: 1;">
                    <strong>${vid.title}</strong>
                    <span class="badge bg-secondary ms-2">${vid.retention_days || 'default'} days</span>
                    ${vid.video_quality ? `<span class="badge bg-primary ms-2">Quality: ${vid.video_quality}</span>` : ''}
                    ${vid.video_format ? `<span class="badge bg-info ms-2">Format: ${vid.video_format}</span>` : ''}
                    ${pruningText}
                    ${downloadedText}
                    ${errorBadge}<br>
                    <small class="text-muted">${vid.url}</small><br>
                    <span class="last-checked">Added: ${formatDate(vid.added_date)} · Last checked: ${formatDate(vid.last_checked)}</span>
                    ${errorSection}
                </div>
                <div class="ms-3">
                    <button class="btn btn-outline-light btn-sm me-2" onclick="openScopedLogs('video', '${vid.id}', '${escapeHtml(vid.title || vid.id)}')">
                        <i class="bi bi-journal-text"></i> Logs
                    </button>
                    <button class="btn btn-warning btn-sm me-2" onclick="openEditVideoModal('${vid.id}', '${escapeHtml(vid.title)}', ${vid.retention_days}, ${vid.disable_pruning}, '${vid.video_quality}', '${vid.video_format}')">
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
        const data = await response.json();
        if (data.success) {
            const videos = data.data || [];
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
                        _currentSingletonVideos[vid.id] = { uploaderID: vid.uploader_id, uploaderName: vid.uploader || vid.uploader_id };
                    }
                }

                // Sort newest added_date first
                items.sort((a, b) => b.sortDate - a.sortDate);

                document.getElementById('videosList').innerHTML = items.map((item, idx) => {
                    if (item.type === 'group') {
                        return renderVideoGroup(item, `video-group-${idx}`);
                    }
                    return renderVideoRow(item.vid);
                }).join('');
            }
        }
    } catch (error) {
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
            document.getElementById('fileNamePattern').value = config.file_name_pattern;
            document.getElementById('maxConcurrent').value = config.max_concurrent_downloads;
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
            return `<div class="log-line" style="border-left-color:${scopeColor};">
                <span class="scope-pill" style="background:${scopeColor};">${escapeHtml(label)}</span>${escapeHtml(entry.line || '')}
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
    const name = document.getElementById('channelName').value;
    const url = document.getElementById('channelURL').value;
    const retention = parseInt(document.getElementById('channelRetention').value) || 0;
    const cutoffDate = document.getElementById('channelCutoffDate').value;
    const quality = document.getElementById('channelQuality').value;
    const format = document.getElementById('channelFormat').value; // Empty string = use global default
    const downloadShorts = document.getElementById('channelDownloadShorts').checked;
    const disablePruning = document.getElementById('channelDisablePruning').checked;

    const channelData = {
        name,
        url,
        retention_days: retention,
        disable_pruning: disablePruning,
        video_quality: quality,
        video_format: format,
        download_shorts: downloadShorts
    };
    if (cutoffDate) {
        channelData.cutoff_date = new Date(cutoffDate).toISOString();
    }

    try {
        const response = await fetch(`${API_BASE}/channels`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(channelData)
        });
        const data = await response.json();
        if (data.success) {
            showToast('Channel added successfully');
            document.getElementById('addChannelForm').reset();
            loadChannels();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add channel', true);
        }
    } catch (error) {
        showToast('Failed to add channel', true);
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
function openEditChannelModal(id, name, retentionDays, disablePruning, cutoffDate, videoQuality, videoFormat, downloadShorts) {
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

    const updateData = {
        retention_days: retentionDays,
        disable_pruning: disablePruning,
        video_quality: videoQuality,
        video_format: videoFormat,
        download_shorts: downloadShorts
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
    const url = document.getElementById('videoURL').value;
    const retention = parseInt(document.getElementById('videoRetention').value) || 0;
    const quality = document.getElementById('videoQuality').value;
    const format = document.getElementById('videoFormat').value; // Empty string = use global default
    const disablePruning = document.getElementById('videoDisablePruning').checked;

    try {
        const response = await fetch(`${API_BASE}/videos`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                url,
                retention_days: retention,
                disable_pruning: disablePruning,
                video_quality: quality,
                video_format: format
            })
        });
        const data = await response.json();
        if (data.success) {
            showToast('Video added successfully');
            document.getElementById('addVideoForm').reset();
            loadVideos();
            loadStatus();
        } else {
            showToast(data.message || 'Failed to add video', true);
        }
    } catch (error) {
        showToast('Failed to add video', true);
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
function openEditVideoModal(id, title, retentionDays, disablePruning, videoQuality, videoFormat) {
    document.getElementById('editVideoId').value = id;
    document.getElementById('editVideoTitle').value = title;
    document.getElementById('editVideoRetention').value = retentionDays || '';
    document.getElementById('editVideoDisablePruning').checked = !!disablePruning;
    document.getElementById('editVideoQuality').value = videoQuality || '';
    document.getElementById('editVideoFormat').value = videoFormat || '';

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
        video_format: videoFormat
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
        file_name_pattern: document.getElementById('fileNamePattern').value,
        max_concurrent_downloads: parseInt(document.getElementById('maxConcurrent').value),
        default_video_format: document.getElementById('defaultVideoFormat').value || 'mp4',
        default_video_quality: document.getElementById('defaultVideoQuality').value,
        yt_dlp: {
            update_interval_seconds: updateIntervalStr,
            extractor_sleep_interval_seconds: sleepIntervalStr,
            download_throughput_limit: document.getElementById('downloadThroughputLimit').value.trim(),
            restrict_filenames: document.getElementById('restrictFilenames').checked,
            cache_dir: document.getElementById('ytDlpCacheDir').value.trim(),
            cookies_browser: document.getElementById('cookiesBrowser').value,
        }
    };

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

// Initial load
loadStatus();
loadChannels();
loadVideos();
loadConfig();
loadLogs();

// Refresh data every 30 seconds
setInterval(() => {
    loadStatus();
    loadChannels();
    loadVideos();
    loadLogs();
}, 30000);
