import os
import discord
import docker
from discord import app_commands

TOKEN = os.environ["DISCORD_TOKEN"]
GUILD_ID = os.environ["SERVER_ID"]

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
docker_client = docker.from_env()

@bot.event
async def on_ready():
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
        running = docker_client.containers.list()
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
