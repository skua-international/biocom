import os
import discord
import docker
import re
from urllib.parse import unquote
from discord import app_commands
from docker.errors import DockerException

TOKEN = os.environ["DISCORD_TOKEN"]
GUILD_ID = os.environ["SERVER_ID"]

UPLOAD_BASE = "/app/uploads"
MISSIONS_DIR = f"{UPLOAD_BASE}/missions"
PRESETS_DIR = f"{UPLOAD_BASE}/presets"

SAFE_FILENAME_RE = re.compile(r"[^A-Za-z0-9 ._-]")

def sanitize_filename(raw_name: str) -> str:
    """
    Decode URL-encoded names and strip unsafe characters.
    Preserves spaces, dots, underscores, and dashes.
    """
    decoded = unquote(raw_name)
    decoded = os.path.basename(decoded)  # kill any path components
    sanitized = SAFE_FILENAME_RE.sub("", decoded)
    return sanitized.strip()

def get_container_client():
    try:
        return docker.from_env()
    except DockerException as e:
        return None

class Bot(discord.Client):
    def __init__(self):
        intents = discord.Intents.default()
        super().__init__(intents=intents)
        self.tree = app_commands.CommandTree(self)

    async def setup_hook(self):
        guild = discord.Object(id=GUILD_ID)
        self.tree.copy_global_to(guild=guild)
        await self.tree.sync(guild=guild)

bot = Bot()

@bot.event
async def on_ready():
    docker_client = get_container_client()
    if docker_client is None:
        print("BIOCOM: FAILED TO CONNECT TO CONTAINER RUNTIME.")
        return

    info = docker_client.info()

    runtime = "podman" if "podman" in info.get("OperatingSystem", "").lower() else "docker"
    print(f"BIOCOM attached to {runtime.upper()}")
    print(f"Logged in as {bot.user} (ID: {bot.user.id})")

    # KEEPING YOUR ACTIVITY EXACTLY
    await bot.change_presence(
        status=discord.Status.dnd,
        activity=discord.Game(
            name="CLCTR MULTITHREAD PROCESSOR ACTIVATED"
        )
    )

# ---------------- SLASH COMMANDS ----------------

@bot.tree.command(name="ping", description="Check if BIOCOM is alive")
async def ping(interaction: discord.Interaction):
    await interaction.response.send_message(
        "BIOCOM: STATUS OPERATIONAL. GLOBAL LOOP.",
        ephemeral=True
    )

@bot.tree.command(name="intercept", description="intercept communication")
@app_commands.describe(
    message="Post a message to the specified channel or thread",
    channel="Optional channel or thread (defaults to current)"
)
async def intercept(
    interaction: discord.Interaction,
    message: str,
    channel: discord.abc.GuildChannel | discord.Thread | None = None,
):
    await interaction.response.defer(thinking=True, ephemeral=True)

    target = channel or interaction.channel

    # Check if the user has permission to send messages in the target channel
    permissions = target.permissions_for(interaction.user)
    if not permissions.send_messages:
        await interaction.followup.send(
            "BIOCOM: INSUFFICIENT CLEARANCE FOR TRANSMISSION.",
            ephemeral=True
        )
        return
    
    # Check if the user has Zeus role (role name: "Zeus")
    zeus_role = discord.utils.get(interaction.guild.roles, name="Zeus")
    if zeus_role not in interaction.user.roles:
        await interaction.followup.send(
            "BIOCOM: UNAUTHORIZED OPERATOR. ACCESS DENIED.",
            ephemeral=True
        )
        return

    # Safety check
    if not hasattr(target, "send"):
        await interaction.followup.send(
            "BIOCOM: CANNOT TRANSMIT TO TARGET.",
            ephemeral=True
        )
        return

    async with target.typing():
        await target.send(message)

    await interaction.followup.send(
        "BIOCOM: MESSAGE SENT.",
        ephemeral=True
    )

@bot.tree.command(name="upload_mission", description="Upload a mission (.pbo)")
@app_commands.describe(file="Mission file (.pbo)")
@app_commands.checks.has_role("Zeus")
async def upload_mission(
    interaction: discord.Interaction,
    file: discord.Attachment,
):
    print(f"Mission upload from {interaction.user}: {file.filename}, {file.size} bytes, url: {file.url}")
    await interaction.response.defer(ephemeral=True)

    if not file.filename.lower().endswith(".pbo"):
        await interaction.followup.send(
            "BIOCOM: INVALID FILE TYPE. EXPECTED `.pbo`.",
            ephemeral=True
        )
        return

    safe_name = sanitize_filename(file.filename)

    if not safe_name.lower().endswith(".pbo"):
        await interaction.followup.send(
            "BIOCOM: INVALID FILENAME AFTER SANITIZATION.",
            ephemeral=True
        )
        return

    save_path = os.path.join(MISSIONS_DIR, safe_name)

    # Save to disk
    await file.save(save_path)

    # Re-post attachment publicly (NON-EPHEMERAL)
    await interaction.channel.send(
        content="BIOCOM: MISSION INGESTED.",
        file=discord.File(save_path, filename=safe_name)
    )

    # Ephemeral confirmation
    await interaction.followup.send(
        f"BIOCOM: MISSION `{safe_name}` STORED AND BROADCAST.",
        ephemeral=True
    )

@bot.tree.command(name="upload_preset", description="Upload a preset (.html)")
@app_commands.describe(file="Preset file (.html)")
@app_commands.checks.has_role("Zeus")
async def upload_preset(
    interaction: discord.Interaction,
    file: discord.Attachment,
):
    print(f"Preset upload from {interaction.user}: {file.filename}, {file.size} bytes, url: {file.url}")
    await interaction.response.defer(ephemeral=True)

    if not file.filename.lower().endswith(".html"):
        await interaction.followup.send(
            "BIOCOM: INVALID FILE TYPE. EXPECTED `.html`.",
            ephemeral=True
        )
        return

    safe_name = sanitize_filename(file.filename)

    if not safe_name.lower().endswith(".html"):
        await interaction.followup.send(
            "BIOCOM: INVALID FILENAME AFTER SANITIZATION.",
            ephemeral=True
        )
        return

    save_path = os.path.join(PRESETS_DIR, safe_name)
    await file.save(save_path)

    await interaction.channel.send(
        content="BIOCOM: PRESET INGESTED.",
        file=discord.File(save_path, filename=safe_name)
    )

    await interaction.followup.send(
        f"BIOCOM: PRESET `{safe_name}` STORED AND BROADCAST.",
        ephemeral=True
    )


@bot.tree.command(
    name="containers",
    description="List running Docker containers on the BIOCOM stack"
)
@app_commands.checks.has_role("Server Admin")
@app_commands.checks.has_permissions(administrator=True)
async def containers(interaction: discord.Interaction):
    if interaction.guild_id != int(GUILD_ID):
        await interaction.response.send_message(
            "BIOCOM: COMMAND UNAVAILABLE.",
            ephemeral=True
        )
        return

    await interaction.response.defer(ephemeral=True)

    try:
        running = get_container_client().containers.list()
    except Exception as e:
        await interaction.followup.send(
            f"BIOCOM: DOCKER ACCESS FAILURE.\n{e}",
            ephemeral=True
        )
        return

    if not running:
        await interaction.followup.send(
            "BIOCOM: NO ACTIVE CONTAINERS DETECTED.",
            ephemeral=True
        )
        return

    lines = []
    for c in running:
        name = c.name
        image = c.image.tags[0] if c.image.tags else c.image.short_id
        status = c.status
        lines.append(f"• `{name}` — `{image}` — `{status}`")

    output = "\n".join(lines)

    # Discord message limit safety
    if len(output) > 1900:
        output = output[:1900] + "\n…truncated"

    await interaction.followup.send(
        f"BIOCOM: ACTIVE CONTAINERS\n{output}",
        ephemeral=True
    )

bot.run(TOKEN)
