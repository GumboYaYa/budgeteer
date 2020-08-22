# Generated by Django 3.0.7 on 2020-08-21 14:43

from django.db import migrations, models
import django.db.models.deletion


class Migration(migrations.Migration):

    initial = True

    dependencies = [
    ]

    operations = [
        migrations.CreateModel(
            name='Bic',
            fields=[
                ('id', models.AutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('bic', models.CharField(max_length=11, unique=True)),
            ],
        ),
        migrations.CreateModel(
            name='BookingType',
            fields=[
                ('id', models.AutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('name', models.CharField(max_length=50, unique=True)),
            ],
        ),
        migrations.CreateModel(
            name='Currency',
            fields=[
                ('id', models.AutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('currency_short', models.CharField(max_length=3, unique=True)),
                ('currency_long', models.CharField(max_length=100)),
            ],
        ),
        migrations.CreateModel(
            name='Iban',
            fields=[
                ('id', models.AutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('iban', models.CharField(max_length=22, unique=True)),
            ],
        ),
        migrations.CreateModel(
            name='Optionee',
            fields=[
                ('id', models.AutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('name', models.CharField(max_length=200)),
                ('bic', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, to='main.Bic')),
                ('iban', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, to='main.Iban')),
            ],
        ),
        migrations.CreateModel(
            name='Transaction',
            fields=[
                ('id', models.AutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('date_booking', models.DateField()),
                ('date_imported', models.DateField(auto_now_add=True)),
                ('figure', models.DecimalField(decimal_places=2, max_digits=18)),
                ('reference', models.CharField(max_length=500)),
                ('account', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, to='main.Iban')),
                ('booking_type', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, to='main.BookingType')),
                ('currency', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, to='main.Currency')),
                ('optionee', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, to='main.Optionee')),
            ],
        ),
    ]
